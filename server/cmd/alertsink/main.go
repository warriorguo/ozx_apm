// Command alertsink is a webhook receiver for checking APM-8 alerting end to
// end without a chat tool: it prints every POST it receives (time, headers
// of interest, the JSON body indented) and answers 200.
//
//	go run ./cmd/alertsink -addr :9099
//	OZX_ALERT_ENABLED=true OZX_ALERT_WEBHOOK_URL=http://<host>:9099/hook ./ozx-apm-server
//
// -fail N answers 500 to the first N requests (to see the evaluator retry
// with backoff); -feishu answers like Feishu ({"code":0}).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9099", "listen address")
	fail := flag.Int("fail", 0, "answer 500 to the first N requests")
	feishu := flag.Bool("feishu", false, `answer {"code":0} like a Feishu bot`)
	flag.Parse()

	var mu sync.Mutex
	n := 0
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		mu.Lock()
		n++
		i := n
		mu.Unlock()
		var pretty bytes.Buffer
		if json.Indent(&pretty, body, "", "  ") != nil {
			pretty.Write(body)
		}
		status := http.StatusOK
		if i <= *fail {
			status = http.StatusInternalServerError
		}
		fmt.Printf("=== #%d %s %s %s (Content-Type %q, User-Agent %q) -> %d\n%s\n",
			i, time.Now().UTC().Format(time.RFC3339), r.Method, r.URL.Path,
			r.Header.Get("Content-Type"), r.Header.Get("User-Agent"), status, pretty.String())
		w.WriteHeader(status)
		if *feishu {
			io.WriteString(w, `{"code":0,"msg":"success"}`)
		}
	})
	log.Printf("alertsink listening on %s (fail first %d)", *addr, *fail)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
