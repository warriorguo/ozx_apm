# OZX APM

Application Performance Monitoring system for Unity games. Track performance, crashes, and user experience across your player base.

## Features

- **Performance Monitoring**: FPS distribution, frame time analysis, jank detection
- **Crash & Exception Tracking**: Stack traces, fingerprinting, breadcrumb context
- **Startup Timing**: Multi-phase tracking (app launch → Unity init → first frame → TTI)
- **Scene Load Tracking**: Load and activation timing per scene
- **Memory Monitoring**: GC tracking, allocation monitoring
- **Offline Support**: Queue events when offline, retry on reconnect
- **Operations data**: player-level events (DAU, retention, funnels, rewarded-ad revenue) counted exactly once, kept a year apart from the diagnostics, analysed in Metabase (APM-12)

## Architecture

```
┌─────────────────┐     HTTPS/gzip      ┌─────────────────┐
│   Unity Game    │ ──────────────────► │   Go Server     │
│   (SDK)         │     JSON events     │   (API)         │
└─────────────────┘                     └────────┬────────┘
                                                 │
                                                 ▼
                                        ┌─────────────────┐
                                        │   ClickHouse    │
                                        │   (Storage)     │
                                        └─────────────────┘
```

## Quick Start

### Server

1. Start ClickHouse:
```bash
cd server/deployments
docker-compose up -d clickhouse
```

2. Run the server:
```bash
cd server
go run ./cmd/server
```

3. Verify health:
```bash
curl http://localhost:8080/health
```

### Unity SDK

1. Add the package to your Unity project via Package Manager:
   - Window → Package Manager → Add package from disk
   - Select `sdk/package.json`

2. Initialize in your startup script:
```csharp
using OzxApm.Core;

public class GameInit : MonoBehaviour
{
    void Awake()
    {
        ApmClient.Initialize(
            serverUrl: "http://your-server:8080",
            appKey: "your-app-key",
            appVersion: Application.version
        );
    }

    void Start()
    {
        // Call when game is ready for player interaction
        ApmClient.MarkTTI();
    }
}
```

## Configuration

### Server Configuration

Copy `server/config.yaml.example` to `server/config.yaml`:

```yaml
server:
  host: "0.0.0.0"
  port: 8080

clickhouse:
  host: "localhost"
  port: 9000
  database: "ozx_apm"

auth:
  enabled: true
  app_keys:
    "your-app-key": "Your App Name"

ratelimit:
  enabled: true
  requests_per_min: 1000
```

### SDK Configuration

```csharp
var config = new ApmConfig
{
    ServerUrl = "https://your-server.com",
    AppKey = "your-app-key",
    AppVersion = "1.0.0",

    // Feature toggles
    EnablePerformance = true,
    EnableJankDetection = true,
    EnableExceptionCapture = true,
    EnableStartupTiming = true,
    EnableSceneLoadTracking = true,

    // Sampling
    SamplingIntervalSeconds = 1.0f,
    // Jank (APM-21): consecutive slow frames (> 1.5x the recent median frame
    // time, at least +8 ms) lasting more than this; at most MaxJanksPerMinute sent
    JankThresholdMs = 50f,
    MaxJanksPerMinute = 20,

    // Batching
    BatchSize = 20,
    FlushIntervalSeconds = 30f,
    // Perf samples and other low-value data go out at most this often while idle
    // (max 600 s); exceptions, crashes and startup events are never delayed by it,
    // and everything is flushed and uploaded on pause/background/quit.
    LowValueUploadIntervalSeconds = 120f,

    // Offline support
    EnableOfflineStorage = true,
    MaxOfflineStorageBytes = 5 * 1024 * 1024,
    // Repeat counts of errors, custom events beyond the budget and metric
    // samples wait in memory for their one-minute window; they are written to a
    // checkpoint file at most this often (off the main thread, only after
    // something was counted), so a kill or crash loses at most this much of
    // them (APM-24). 0 turns it off.
    CountCheckpointIntervalSeconds = 5f
};

ApmClient.Initialize(config);
```

### Game context

Key/value pairs that every event carries from the moment they are set (perf
samples, janks, exceptions, crashes, ...), so the admin can filter and group by
them. The snapshot at enqueue time is stored with the event on disk.

```csharp
ApmClient.SetContext("gitCommit", "a1b2c3d");   // works before Initialize
ApmClient.SetContext("levelId", levelId);
ApmClient.SetContext("roomId", roomId);         // a null or empty value removes the key
ApmClient.RemoveContext("roomId");
ApmClient.ClearContext();
```

- Keys: 1-64 characters from `[A-Za-z0-9_.-]`; values are cut to 256 characters;
  at most 64 keys and about 4KB in total. Refused values return `false`, log a
  warning (never an error) and are counted in `ApmClient.GetStats()`.
- Any thread. Setting the value a key already has costs nothing; a change is
  copied once, by the next event. Not meant for values that change every frame.
- Build type: every event also carries `build_type` (`release`, `dev` or
  `editor`; detected, or `ApmConfig.BuildType`), the GPU, a RAM class and the
  network type. The admin shows release builds by default. In the Unity Editor
  events are stored but not uploaded unless `ApmConfig.UploadInEditor = true`.

### Custom events, metrics and timers

```csharp
ApmClient.TrackEvent("room_enter", "roomId", roomId);   // uploaded, and a breadcrumb
ApmClient.RecordMetric("gold_delta", delta, "count");

var t = ApmClient.StartTimer("room_switch_ms");          // a struct: nothing allocated
// ... set the new room's context, then:
ApmClient.StopTimer(t);                                   // or StopTimer("room_switch_ms")
```

- Custom events go onto the breadcrumb time line that later exceptions and
  janks carry, together with the recent Warning/Error log lines (not plain
  logs): an exception shows, in order, e.g. `Event: room_enter roomId=12`,
  `Warning: Asset not found: anim/arack_red`, then the NRE. The first 60 events
  a minute (`ApmConfig.MaxCustomEventsPerMinute`, at most 10 of one name) are
  uploaded one by one with their properties; more are uploaded as counts per
  name and context.
- Metrics and timers are aggregated on the device per name, game context and
  minute (count, sum, min, max, histogram): the admin's Custom page shows
  p50/p95/p99 per context value (roomId...), version or device, within 2%.
- Names and property keys: `[A-Za-z0-9_.-]{1,64}`; property values up to 256
  characters, at most 64 properties (the context's limits). **Never put personal
  data in names, properties or units.** Technical data: collected only with
  technical consent; business events (purchases, funnels) are not for this API.
- Counts and metric windows survive a kill or crash up to the last checkpoint
  (`CountCheckpointIntervalSeconds`, 5 s): the next launch uploads them with
  `recovered = 1`; a window that closes normally replaces its checkpoint (same
  event id: the server never counts both). The same holds for repeat counts of
  errors.

### Memory, game runs and count ranges

Every perf sample (once per `SamplingIntervalSeconds`, never per frame)
carries the memory breakdown (`ApmConfig.EnableMemoryBreakdown`, on by
default): the managed heap in use (`mem_mb`, as before) and reserved,
Unity's native memory allocated and reserved, the graphics memory Unity
estimates, and the process's resident memory (Android: RSS from
`/proc/self/statm`, read on a thread of its own; elsewhere Unity's System
Used Memory). A figure the platform does not give is not sent.

```csharp
ApmClient.BeginRun(runId);       // a game run (OZX: a dungeon run); sets the context key "runId"
// ... the run ...
ApmClient.EndRun("win");         // sends a "run" event: peak of every memory figure, start/end, length
ApmClient.SetContextCount("aliveEnemies", aliveCount);   // "0", "1-5", "6-10", "11-20", ... "201+"
```

- `BeginRun(id)`: any thread; the id is 1-64 printable characters (a run
  sequence number or seed, never personal data). A run still open is ended
  first (`end_reason` `new_run`); one open at quit is ended as `shutdown`.
  `EndRun(result)`: the result is optional (`[A-Za-z0-9_.-]{1,64}`); the
  context key `runId` is removed (if it still is the run's id).
  `ApmClient.CurrentRunId` is the open run.
- If the process dies during a run (an OOM kill, a crash, a kill in the
  background), the crash journal has the run and its peaks: the next launch
  sends the run (`end_reason` `crash` with the crash type, or `process_end`),
  and the crash's details carry the last memory reading, its age, the
  process's peak RSS and the run's peaks ("memory before death" on the crash
  page).
- `SetContextCount(key, n)`: for numbers that change often (alive enemies):
  the context only changes when `n` enters another range, so calling it every
  frame copies and allocates nothing. The admin groups raw numeric context
  values into the same ranges (`bucket=count`).
- Admin: **Memory** (breakdown and peak per run by version, device, RAM class
  or any context key; OOM runs), **Health** (crash-free sessions and users,
  ANR rate, exception rates per version, build type or data pack), **Funnel**
  (custom events in order per session), and the Context page's jank rate per
  minute of play and FPS p05 by any context key. Details: `server/RELEASE.md`
  (APM-9).

### Typed events and the session export (OZX AiTelemetry format)

```csharp
ApmClient.BeginRun(runId);
ApmClient.Event("runstart.run").Send();
ApmClient.Event("playermovement.room_enter").Str("roomId", roomId).Vec("pos", p.x, p.y).Send();
ApmClient.Event("loot.drop").Str("item", item).Str("src", "enemy").Bool("pin", false).Vec("pos", p.x, p.y).Send();
```

- `ApmClient.Event(name)`: a custom event with typed properties (`Str`,
  `Int`, `Num` with 3 fixed decimals by default, `Bool`, `Vec` as `key.x` /
  `key.y`), otherwise `TrackEvent` (limits, budget, time line). Any thread;
  build and send on one thread; nothing allocated when not collecting.
- Every event the SDK records while the game runs carries the game clock:
  `t` (`Time.timeAsDouble`, scaled game time) and `frame`
  (`Time.frameCount`), as OZX's AiTelemetry lines do.
- The admin's session time line has **Download (AiTelemetry JSONL)**
  (`GET /api/sessions/export?session_id=S`): a zip of an AiTelemetry session
  directory (`session.json`, `<channel>.jsonl` for events named
  `<channel>.<ev>`, `custom.jsonl`, `apm.jsonl` for APM's own events) that
  OZX's `scripts/ai_trace.py` reads as is. Events beyond the per-minute
  budget (`MaxCustomEventsPerMinute`, `MaxCustomEventsPerNamePerMinute`) are
  only counted and are not in the channel files. Details, the line format and
  what OZX must send: `server/RELEASE.md` (APM-10).

### Privacy consent

Two categories, each `Undecided`, `Granted` or `Denied`: `Technical`
(exceptions, crashes, performance, custom events and metrics) and `Analytics`
(the operations events of APM-12, below: their own queue, directory and
uploader). Only `Granted` collects; nothing is enqueued, written to disk or
uploaded otherwise. Each category's data is kept and deleted by its own consent.

```csharp
config.DefaultTechnicalConsent = ConsentState.Granted;    // built-in default
config.DefaultAnalyticsConsent = ConsentState.Undecided;  // built-in default
// config.UseOptInConsent();   // opt-in markets: both Undecided, nothing until asked

ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);  // also before Initialize
ApmClient.GetConsent(ConsentCategory.Analytics);
string deviceId = ApmClient.GetDeviceId();   // give it to support for server-side deletion
ApmClient.ResetDeviceId();                   // new id + new session, local events deleted
```

- The player's choice is stored in PlayerPrefs and wins over the defaults on
  the next launches (`ApmConfig.RememberConsent`, on by default).
- `Denied` stops the collectors and deletes every locally stored event and SDK
  file, the debug network log and the stored device/session ids; the upload
  under way is aborted. `Undecided` stops collecting but keeps what is stored.
- Main thread. Details, server-side deletion and the admin grant:
  `server/RELEASE.md` (APM-14).
- Analytics `Denied` deletes only the ops queue and the stored player id;
  technical `Denied` leaves them.

### Operations events and ads (APM-12)

Business events for designers and operations (DAU, retention, funnels, ad
revenue), shown in Metabase (`docs/ops-schema.md`). One call per event; the
SDK never samples, budgets or aggregates them, gives each a unique id (the
server stores it once whatever is re-sent) and keeps it on disk until the
server confirms it. **Only with analytics consent.**

```csharp
var config = new ApmConfig { /* ... */ };
config.DefaultAnalyticsConsent = ConsentState.Granted;  // where no opt-in is needed; built-in default: Undecided
config.Channel = "googleplay";                          // install channel, per build flavor
// config.Country = "DE";   // default: the device's region setting (Android); never a location
ApmClient.Initialize(config);

ApmClient.OpsEvent("run_start").Str("runId", runId).Int("chapter", 3).Send();
ApmClient.OpsEvent("run_end").Str("result", "win").Num("duration_s", 312.5, 1).AlsoTechnical().Send();

// Rewarded ads (OZX-1280): fixed names and keys, so the Ads dashboard works as is.
ApmClient.SetContext("chapter", "3"); ApmClient.SetContext("room", roomId); ApmClient.SetContext("revivesUsed", "1");
ApmAds.EntryShown("revive");                      // the offer became visible
ApmAds.Click("revive");                           // the player chose to watch
ApmAds.Request("revive", "admob");                // optional
ApmAds.Show("revive", "admob", revenue, "USD");   // impression + the ad SDK's estimated revenue (currency units, not micros)
ApmAds.Complete("revive", "admob");
ApmAds.Reward("revive", "admob");
ApmAds.Fail("shop", "unity", "no_fill");          // any failure, a short reason code

string playerId = ApmClient.GetPlayerId();   // what support needs to delete a player's ops data
ApmClient.ResetPlayerId();                   // a new player: local ops events deleted, new session
```

- Names and property keys: `[A-Za-z0-9_.-]{1,64}`; properties typed like
  `ApmClient.Event` (`Str`, `Int`, `Num`, `Bool`, `Vec`); never personal data.
  Any thread; `Send()` returns false when nothing was recorded.
- Sent with each event: name, properties, the anonymous **player id** (random,
  stable across launches, stored with analytics consent; not the device id; a
  reinstall is a new player), session, version, platform, build type, install
  channel, a coarse country and the game context (the server keeps the keys of
  its whitelist: `runId`, `chapter`, `levelId`, `room`, `roomId`,
  `revivesUsed` by default). Never the device id, device details or scene.
- Automatic: `apm_session_start` at every session start and
  `apm_session_end` (`foreground_ms`) when the game goes to the background or
  quits (`ApmConfig.OpsSessionEvents`): DAU, new players, retention and session
  length need no game code.
- With technical consent an ops event is also a breadcrumb on the time line;
  `AlsoTechnical()` also records it as a technical custom event (session time
  line, export). Each copy follows its own consent.
- Ad events: `placement` is the game's slot name, `network` the ad network in
  lower case (the same names as in the imported network reports), `revenue` the
  ad SDK's per-impression estimate in `currency` (default USD). The run's
  context keys above come with every ad event.
- Storage: `<persistentDataPath>/ozx_apm_ops`, at most
  `ApmConfig.MaxOpsStorageBytes` (4 MB; the oldest are dropped beyond it,
  `ApmStats.OpsDroppedEvents`). `ApmConfig.EnableOpsEvents = false` turns it all off.
- Details (server, BI, time rules, privacy): `server/RELEASE.md` (APM-12).

### Crashes, ANRs and kills (Android)

On by default on the Android player (`ApmConfig.EnableCrashReporting`), with
technical consent only. While the game runs, the SDK keeps a small crash
journal on disk: session, version, scene, foreground/background, the game
context and the breadcrumb time line (custom events, scene loads, recent
warnings and errors). It is rewritten on its own thread, at most every
0.1 s and only when something changed. At the next launch the SDK reports
how the previous process ended, with that context:

| Android says (11+) | Reported as |
|---|---|
| `CRASH_NATIVE` (native or IL2CPP crash; the stack from the tombstone on 12+) | `native` |
| `ANR` (with the main and UnityMain threads of the ANR trace) | `anr` |
| an ANR trace on any other exit (an ANR the process survived; time of the ANR) | `anr` (plus the exit itself if that was a crash) |
| `CRASH` with Unity's own tombstone of that process (a native crash ActivityManager killed first) | `native` |
| `CRASH` (Java) | `java` |
| `LOW_MEMORY` while in the foreground | `oom` |
| killed while in the foreground (`SIGNALED`, `EXCESSIVE_RESOURCE_USAGE`, ...) | `killed` |
| nothing (Android 10 and older), but the game was in the foreground | `unknown` |

A process reclaimed in the background, `Application.Quit` and a clean
shutdown are not crashes. Nothing to call; `ApmClient.GetStats()` has
`CrashReporting`, `CrashesReported` and the journal's writes.

ANRs are also reported while the game runs (`ApmConfig.EnableAnrWatchdog`,
on by default): a background thread checks the Java main thread once a
second in the foreground (no allocation, no C# on the main thread) and
reports an ANR once Android has declared the process "not responding",
with the main thread's stack. The same ANR found again in the exit record
at the next launch has the same event id (stored once). A frozen game loop
(UnityMain) with a responsive UI thread is not an ANR for Android and is
not reported (see `server/RELEASE.md`, APM-7).

### IL2CPP symbols (C# names, files and lines in native stacks)

A release `libil2cpp.so` has no symbols: a native crash only says
`libil2cpp.so + 0x5417bc`. Upload the build's symbols once per build (CI
step after the Unity build); the admin then shows the C# method and, with
the line map, the file and line, also for crashes stored before the upload.

Build settings (Unity 2022.3, Android):

```csharp
EditorUserBuildSettings.androidCreateSymbols = AndroidCreateSymbols.Debugging;  // <apk>-...symbols.zip: <abi>/libil2cpp.so with debug info
PlayerSettings.SetAdditionalIl2CppArgs("--emit-source-mapping --emit-method-map"); // LineNumberMappings.json, MethodMap.tsv
```

These give native crash frames C# names, files and lines. Managed
**exception** stacks of a 2022.3 IL2CPP release build keep only method
names: they carry no addresses to symbolicate, and Unity's
`Il2CppStacktraceInformation` setting (file:line in managed stacks) exists
from Unity 2023.1 only.

Upload (Go 1.21+; the token is the admin server's `OZX_SYMBOLS_UPLOAD_TOKEN`):

```bash
cd server && go build -o apmsym ./cmd/apmsym
export OZX_SYMBOLS_UPLOAD_TOKEN=...            # from the secret store
./apmsym upload -url https://<admin-host> -app-version 1.2.3 -app-build 45 \
    Builds/ozx-1.2.3-v45-IL2CPP.symbols.zip \
    Builds/ozx_BackUpThisFolder_ButDontShipItWithYourGame/il2cppOutput
./apmsym extract <same paths>                  # what would be uploaded, nothing sent
./apmsym lookup <same paths> 0x56a030 0x5726b8   # file:line as the admin will show them (callers at pc-1; -frame0: the first is the faulting frame)
```

It reads ELF libraries (`symbols.zip`, whose entries are named
`<abi>/libil2cpp.so`, `libunity.so`, `libmain.so`; also `*.sym.so` /
`*.dbg.so`), `MethodMap.tsv` and `LineNumberMappings.json` (searched under
the given directories; IL2CPP writes them to
`<build>_BackUpThisFolder_ButDontShipItWithYourGame/il2cppOutput/Symbols/`), keeps per library a compact table (functions; C# line
ranges), and uploads it by the library's ELF build id. Details, limits and
what each file adds: `server/RELEASE.md` (APM-7).

## API Endpoints

### Ingestion

**POST /v1/events** - Submit event batch
```bash
curl -X POST http://localhost:8080/v1/events \
  -H "Content-Type: application/json" \
  -H "X-App-Key: your-app-key" \
  -d '{"events":[{"type":"perf_sample","timestamp":1234567890000,"fps":60}]}'
```

### Queries

**GET /v1/metrics/fps** - FPS distribution
```bash
curl "http://localhost:8080/v1/metrics/fps?app_version=1.0.0&platform=Android"
```

**GET /v1/metrics/startup** - Startup time percentiles
```bash
curl "http://localhost:8080/v1/metrics/startup?app_version=1.0.0"
```

**GET /v1/metrics/jank** - Jank statistics
```bash
curl "http://localhost:8080/v1/metrics/jank?scene=MainMenu"
```

**GET /v1/exceptions** - Exception list
```bash
curl "http://localhost:8080/v1/exceptions?app_version=1.0.0&limit=50"
```

**GET /v1/crashes** - Crash list
```bash
curl "http://localhost:8080/v1/crashes?platform=Android"
```

### Privacy deletion (admin server)

**POST /api/privacy/delete** - Delete every stored row of a device (APM-14).
Disabled until `OZX_ADMIN_PRIVACY_TOKEN` is set; needs `ALTER DELETE` for the
ClickHouse account; see `server/RELEASE.md`.
```bash
curl -X POST http://localhost:8081/api/privacy/delete -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -H "X-Apm-Confirm-Delete: $DEVICE_ID" \
  -d "{\"id_type\":\"device_id\",\"id\":\"$DEVICE_ID\"}"
```

**POST /api/privacy/verify** - Rows left per table and the deletion requests
```bash
curl -X POST http://localhost:8081/api/privacy/verify -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d "{\"id_type\":\"device_id\",\"id\":\"$DEVICE_ID\"}"
```

`"id_type":"player_id"` (APM-12, `ApmClient.GetPlayerId`) deletes a player's
operations data (`ops_events`, `ops_player_days`, `ops_players`); a player's
technical data is under the device id: delete both.

### Operations data (admin server, APM-12)

**POST /api/ops/import/fx-rates**, **POST /api/ops/import/ad-report** - daily
exchange rates and the ad networks' daily reports (CSV), for the BI's revenue in
USD and its reconciliation. Bearer token `OZX_ADMIN_OPS_TOKEN`; disabled (403)
without one. `go run ./cmd/opsimport` sends a file. BI (Metabase, preset
dashboards: `go run ./cmd/metabase-provision`), the read-only ClickHouse account
and the time rules: `server/RELEASE.md` (APM-12); tables for query writers:
`docs/ops-schema.md`.

### Alerting and issue state (admin server)

Alerts (new exception/crash fingerprint, regression of a resolved issue,
crash rate spike, startup P95 regression) are evaluated from ClickHouse and
posted to one webhook (generic JSON with a `text` field, Slack or Feishu).
Off by default; enable on **one** deployment with `OZX_ALERT_ENABLED=true`
and `OZX_ALERT_WEBHOOK_URL` (a secret), plus `OZX_ALERT_WEBHOOK_FORMAT` and
`OZX_ALERT_ADMIN_BASE_URL` for links. Rules, payload schema, deployment and
verification: `server/RELEASE.md` (APM-8). `go run ./cmd/alertsink` prints
what a webhook would receive.

**POST /api/issues/state** - Mark an exception or crash group resolved (with
the version that has the fix) or unresolved; `GET` reads it.
```bash
curl -X POST http://localhost:8081/api/issues/state -H "Content-Type: application/json" -H "X-Apm-Admin: 1" \
  -d '{"kind":"exception","fingerprint":"<fp>","status":"resolved","resolved_in_version":"1.5.0"}'
```

**GET /api/alerts** - Recent alerts and their delivery status.

**POST /api/alerts/test** - Queue a test alert (sent by the evaluator at its
next evaluation). Disabled until `OZX_ADMIN_ALERT_TOKEN` is set.
```bash
curl -X POST http://localhost:8081/api/alerts/test -H "Authorization: Bearer $ALERT_TOKEN"
```

### Symbols (admin server)

**POST /api/symbols/part**, **POST /api/symbols/commit** - Upload of a
library's symbol table by `apmsym` (APM-7). Bearer token
`OZX_SYMBOLS_UPLOAD_TOKEN`; disabled (403) without one.

**GET /api/symbols?build_id=&app_version=&module=** - Uploaded symbols.

**GET /api/crashes/detail?fingerprint=F** - A crash group with its latest
crash symbolicated (C# method, file, line), exit info, context and breadcrumbs.

### Memory, health and funnels (admin server, APM-9)

All take the common filter (time range, at most 31 days; `app_version`,
`platform`, `build_type`, `ctx=key:value`) and run at most two at once.

- **GET /api/memory/breakdown[?by=..|key=..]** - perf samples' memory figures (p50/p95/max) per group
- **GET /api/memory/runs?metric=peak_rss[&by=..|key=..]** - peak memory per run, OOM/crashed runs, histogram
- **GET /api/health/sessions[?by=..|key=dataVersion][&crash_types=native,java,oom,killed][&user_kills=1]** - crash-free sessions and users (force-stops by the player are not crashes unless `user_kills=1`), ANR and exception rates
- **GET /api/funnel?steps=[{"name":"run_start"},{"name":"room_enter","props":{"roomType":"boss"}}][&window=7200][&by=app_version]** - sessions per step and conversion
- **GET /api/context/distribution?key=aliveEnemies&metric=fps&bucket=count** - also `metric=jank_rate`

### Session export (admin server, APM-10)

**GET /api/sessions/export?session_id=S[&at=T][&start_time&end_time]** - a
zip of the session in OZX's AiTelemetry session directory format (range as
the time line's: at +- 24 h, else 7 days, at most 31; 404 if the session has
no row in it). One export at a time; limits and format: `server/RELEASE.md`.
```bash
curl -o session.zip "http://localhost:8081/api/sessions/export?session_id=$SESSION_ID"
unzip session.zip -d /tmp/s && python3 scripts/ai_trace.py /tmp/s/*/
```

## Event Types

| Type | Description |
|------|-------------|
| `perf_sample` | FPS, frame time, memory usage |
| `jank` | A stutter: slow frames relative to the frame rate (APM-21; not resume, scene-load or frame-rate-change frames), with context and recent events |
| `startup` | App launch timing phases |
| `scene_load` | Scene load and activation time |
| `exception` | Non-fatal exceptions |
| `crash` | Native crashes, ANRs, kills of the previous process, with its context and breadcrumbs (APM-7) |
| `custom_event` | `ApmClient.TrackEvent` (APM-6); with `"ops":true` an operations event (APM-12), stored in `ops_events` only |
| `custom_metric` | `RecordMetric` / timers, aggregated per name, context and minute (APM-6) |
| `run` | A game run that ended (`BeginRun` / `EndRun`), with its peak memory (APM-9) |

Other types (`asset_load`, `http`: planned) are rejected with a reason in the
response (`errors`, `rejected_types`).

## Performance Budget

The SDK is designed for minimal overhead:

- **CPU**: < 0.3ms/frame steady state
- **Memory**: < 5MB resident
- **Network**: Configurable daily cap (default < 2MB/DAU)
- **No GC spikes**: Uses object pooling and ring buffers

## Development

### Server Tests
```bash
cd server
go test -short ./...        # Unit tests
go test ./...               # All tests (requires ClickHouse)
```

### SDK Tests
Run via Unity Test Runner (Window → General → Test Runner)

## Project Structure

```
ozx_apm/
├── sdk/                    # Unity SDK (UPM package)
│   ├── Runtime/
│   │   ├── Core/          # ApmClient, Config, Session
│   │   ├── Collectors/    # Performance, Jank, Memory, etc.
│   │   ├── Network/       # EventReporter, OfflineStorage
│   │   ├── Models/        # Event definitions
│   │   └── Utils/         # RingBuffer, DeviceInfo
│   └── Tests/
│
├── server/                 # Go backend
│   ├── cmd/server/        # Entry point
│   ├── cmd/opsimport/     # Imports rates and ad network reports (APM-12)
│   ├── cmd/metabase-provision/  # Creates the BI dashboards in Metabase (APM-12)
│   ├── internal/
│   │   ├── api/           # HTTP handlers, middleware
│   │   ├── models/        # Event structs
│   │   ├── storage/       # ClickHouse repository
│   │   ├── processor/     # Validation, enrichment
│   │   ├── opsbi/         # The BI questions' SQL and dashboards (APM-12)
│   │   └── alert/         # Alert evaluation
│   ├── deployments/clickhouse/users.d/  # The read-only BI account (APM-12)
│   └── tests/
│
├── docs/ops-schema.md     # The operations tables, for query writers
└── CLAUDE.md              # Project documentation
```

## License

MIT
