package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/warriorguo/ozx_apm/server/internal/api/handlers"
	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// APM-7 device round 2: one ANR reported live by the watchdog and again at
// the next launch from the exit record's ANR trace, with the same event_id but
// another timestamp (the trace's dump time) and another stack (no UnityMain,
// "Unknown Source" vs "unavailable", "$Proxy4.run" vs "Runnable.run"), was
// stored twice in two groups. It must be one row, one group, count 1.
const anrLiveStack = "Thread \"main\" tid=12318 crashed\n" +
	"at java.lang.Thread.sleep(Native Method)\n" +
	"at com.unity3d.player.ReflectionHelper.nativeProxyInvoke(Native Method)\n" +
	"at com.unity3d.player.ReflectionHelper.-$$Nest$smnativeProxyInvoke(Unknown Source:0)\n" +
	"at com.unity3d.player.L.invoke(Unknown Source:22)\n" +
	"at java.lang.reflect.Proxy.invoke(Proxy.java:1006)\n" +
	"at $Proxy4.run(Unknown Source)\n" +
	"at android.os.Handler.handleCallback(Handler.java:995)\n" +
	"at android.os.Looper.loop(Looper.java:363)\n"

const anrTraceStack = "Thread \"main\" tid=12318 crashed\n" +
	"at java.lang.Thread.sleep(Native method)\n" +
	"at com.unity3d.player.ReflectionHelper.nativeProxyInvoke(Native method)\n" +
	"at com.unity3d.player.ReflectionHelper.-$$Nest$smnativeProxyInvoke(unavailable:0)\n" +
	"at com.unity3d.player.L.invoke(unavailable:22)\n" +
	"at java.lang.reflect.Proxy.invoke(Proxy.java:1006)\n" +
	"at java.lang.Runnable.run(Runnable.java:-2)\n" +
	"at android.os.Handler.handleCallback(Handler.java:995)\n" +
	"at android.os.Looper.loop(Looper.java:363)\n" +
	"Thread \"UnityMain\" tid=12334\n" +
	"#00 pc 000000000009371c /apex/com.android.runtime/lib64/bionic/libc.so (syscall+28) (BuildId: fa6ae44f4b2b6a34abebf1b3983cbf88)\n" +
	"#01 pc 00000000004624ec libunity.so (BuildId: aa90508360cec411)\n" +
	"#02 pc 0000000000440330 libunity.so (BuildId: aa90508360cec411)\n"

func TestCrashAnr_WatchdogThenExitTrace_OneRowOneGroup(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupCrashEnv(t, async)
			const id = "457bd0efc909ec852ee6708439451b55"
			anr := func(ts int64, stack string, details map[string]string, extra map[string]interface{}) string {
				f := map[string]interface{}{
					"type": "crash", "crash_type": "anr", "fingerprint": "sdk-fp", "event_id": id, "timestamp": ts,
					"stack": stack, "abi": "arm64-v8a", "foreground": true, "details": details,
				}
				for k, v := range extra {
					f[k] = v
				}
				return e.event("S-anr", "r-anr", f)
			}
			live := anr(e.ts-4000, anrLiveStack, map[string]string{"detected_by": "watchdog", "trace": "watchdog",
				"cause": "ANR Input dispatching timed out (925aad com.ozx.apmtest/com.unity3d.player.UnityPlayerActivity is not responding. Waited 10000ms for MotionEvent)."}, nil)
			trace := anr(e.ts-8000, anrTraceStack, map[string]string{"detected_by": "exit_info", "trace": "anr", "anr_trace_on": "SIGNALED",
				"cause": "Input dispatching timed out (925aad com.ozx.apmtest/com.unity3d.player.UnityPlayerActivity is not responding. Waited 10000ms for MotionEvent)."},
				map[string]interface{}{"exit_reason": "SIGNALED", "exit_status": 9, "importance": 400})

			e.post(t, live)
			resp := postRaw(t, e, trace)
			if resp.Accepted != 1 || resp.Duplicates != 1 {
				t.Fatalf("second report: %+v", resp)
			}

			var rows uint64
			must(t, e.client.Conn().QueryRow(context.Background(), "SELECT count() FROM apm_crashes WHERE event_id = ?", id).Scan(&rows))
			if rows != 1 {
				t.Fatalf("%d rows for one event id", rows)
			}
			var crashes models.CrashListResponse
			getAdmin(t, e.admin, "/api/crashes", e.params, &crashes)
			if len(crashes.Crashes) != 1 || crashes.Crashes[0].Count != 1 {
				t.Fatalf("groups %+v", crashes.Crashes)
			}
			g := crashes.Crashes[0]
			if g.CrashSite != "com.unity3d.player.ReflectionHelper.nativeProxyInvoke" ||
				g.SampleMessage != "ANR: Input dispatching timed out in com.unity3d.player.ReflectionHelper.nativeProxyInvoke" {
				t.Fatalf("group %+v", g)
			}

			// The exit trace alone (no watchdog report) lands in the same group.
			other := strings.Replace(trace, id, "0123456789abcdef0123456789abcdef", 1)
			e.post(t, other)
			getAdmin(t, e.admin, "/api/crashes", e.params, &crashes)
			if len(crashes.Crashes) != 1 || crashes.Crashes[0].Count != 2 || crashes.Crashes[0].Fingerprint != g.Fingerprint {
				t.Fatalf("groups after a second ANR %+v", crashes.Crashes)
			}
		})
	}
}

// postRaw posts events and returns the ingest response (duplicates allowed).
func postRaw(t *testing.T, e *crashEnv, events ...string) handlers.IngestResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewBufferString(`{"events":[`+strings.Join(events, ",")+`]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ingest.ServeHTTP(w, req)
	var resp handlers.IngestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusOK {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	return resp
}
