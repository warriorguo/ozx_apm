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

    // Offline support
    EnableOfflineStorage = true,
    MaxOfflineStorageBytes = 5 * 1024 * 1024
};

ApmClient.Initialize(config);
```

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

## Event Types

| Type | Description |
|------|-------------|
| `perf_sample` | FPS, frame time, memory usage |
| `jank` | Frame drops with context |
| `startup` | App launch timing phases |
| `scene_load` | Scene load and activation time |
| `exception` | Non-fatal exceptions |
| `crash` | Fatal crashes with breadcrumbs |

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
