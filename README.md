# OZX APM

Application Performance Monitoring system for Unity games. Track performance, crashes, and user experience across your player base.

## Features

- **Performance Monitoring**: FPS distribution, frame time analysis, jank detection
- **Crash & Exception Tracking**: Stack traces, fingerprinting, breadcrumb context
- **Startup Timing**: Multi-phase tracking (app launch → Unity init → first frame → TTI)
- **Scene Load Tracking**: Load and activation timing per scene
- **Memory Monitoring**: GC tracking, allocation monitoring
- **Offline Support**: Queue events when offline, retry on reconnect

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
    JankThresholdMs = 50f,

    // Batching
    BatchSize = 20,
    FlushIntervalSeconds = 30f,
    // Perf samples and other low-value data go out at most this often while idle
    // (max 600 s); exceptions, crashes and startup events are never delayed by it,
    // and everything is flushed and uploaded on pause/background/quit.
    LowValueUploadIntervalSeconds = 120f,

    // Offline support
    EnableOfflineStorage = true,
    MaxOfflineStorageBytes = 5 * 1024 * 1024
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

### Privacy consent

Two categories, each `Undecided`, `Granted` or `Denied`: `Technical`
(everything the SDK collects: exceptions, crashes, performance) and
`Analytics` (operations events, APM-12; none collected yet). Only `Granted`
collects; nothing is enqueued, written to disk or uploaded otherwise.

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

## Event Types

| Type | Description |
|------|-------------|
| `perf_sample` | FPS, frame time, memory usage |
| `jank` | Frame drops with context |
| `startup` | App launch timing phases |
| `scene_load` | Scene load and activation time |
| `exception` | Non-fatal exceptions |
| `crash` | Fatal crashes with breadcrumbs |
| `custom_event` | `ApmClient.TrackEvent` (APM-6) |
| `custom_metric` | `RecordMetric` / timers, aggregated per name, context and minute (APM-6) |

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
│   ├── internal/
│   │   ├── api/           # HTTP handlers, middleware
│   │   ├── models/        # Event structs
│   │   ├── storage/       # ClickHouse repository
│   │   ├── processor/     # Validation, enrichment
│   │   └── alert/         # Alert evaluation
│   └── tests/
│
└── CLAUDE.md              # Project documentation
```

## License

MIT
