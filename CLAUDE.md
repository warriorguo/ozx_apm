# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

OZX APM is a Unity game Application Performance Monitoring system consisting of:
- **Unity Client SDK**: UPM package for Android/iOS (PC later), supporting IL2CPP and Mono
- **Server Backend**: Data ingestion, processing (real-time + offline), storage, and analytics

**Core Use Cases**: Locate frame drops/jank, crash diagnosis with breadcrumbs, network latency analysis, version-to-version performance comparison.

## Client SDK (Unity)

### Design Principles
- UPM package for Android/iOS (PC later), supporting IL2CPP and Mono
- Single-line initialization with modular opt-in features
- Low overhead sampling: per-second/per-N-frames (configurable)
- Enhanced sampling only triggers on jank detection
- Offline queue with ring buffer + disk persistence for retry on next launch
- Remote configuration for sampling rates, feature toggles, privacy controls

### Every file and folder under `sdk/` has a `.meta`

OZX pulls the SDK as a git package, which Unity treats as read-only: it never creates a missing
`.meta` there, it ignores the asset instead — a folder without one drops everything in it (APM-27:
`Runtime/` had none, so the `OzxApm` assembly did not exist). When adding a file or folder
without the Unity Editor, add its `.meta` in the same commit (a fresh 32-hex guid from `uuidgen`,
importer by type: `DefaultImporter` + `folderAsset: yes` for folders, `MonoImporter` for `.cs`,
`AssemblyDefinitionImporter` for `.asmdef`). Check: `find sdk -mindepth 1 ! -name '*.meta' | while read f; do [ -e "$f.meta" ] || echo "$f"; done` prints nothing.

### Performance Budgets
- CPU: < 0.3ms/frame steady state
- Memory: < 5MB resident (including queues)
- Network: configurable daily cap (target < 2MB/DAU)
- Must not introduce GC spikes

### ID & Session
- `device_id`: anonymous, resettable, irreversible hash
- `user_id`: optional, business login integration
- `session_id`: foreground/background split rules configurable
- `trace_id`: for correlating network requests and events

## Server Backend

### Data Ingestion
- **Protocol**: HTTPS + gzip, JSON or Protobuf
- **Batching**: 20 events or 64KB per request
- **Auth**: app_key + signature / token
- **Anti-abuse**: rate limiting, blacklist, anomaly payload isolation

### Data Processing (Dual Channel)
- **Real-time** (minute-level): alerts, crash spikes, version regression detection
- **Offline** (hour-level): reports, comparisons, distribution statistics

### Storage (Recommended)
- Time-series/Metrics: ClickHouse / TimescaleDB
- Event details: ClickHouse / Elasticsearch
- Crash clustering index: Elasticsearch / ClickHouse

### Query & Analysis Capabilities
- **Dimension filtering**: version, channel, device model, OS, region, network type
- **Distribution charts**: startup time, scene load, FPS, frame time (P50/P95/P99)
- **Top lists**: exceptions, crashes, jank scenes
- **Drill-down correlation**: version frame drop → common jank scenes → heavy asset loads/GC spikes → related exceptions/network
- **Comparison analysis**: version A vs B on same dimension slice
- **Impact analysis**: event UV, session count, percentage

### Alerting
- Crash rate threshold (by version/channel)
- ANR rate / jank rate threshold
- Startup P95 regression (% increase vs previous version)
- Network failure rate spike (by region/carrier)
- Alert deduplication and suppression to prevent storms

## Event Data Model

### Common Fields (every event)
```
timestamp, app_version, build, unity_version
platform, os_version, device_model, cpu, gpu, ram_class
session_id, device_id, user_id (optional)
scene, level_id (optional)
net_type, country (optional)
```

### Event Types
| Type | Key Fields |
|------|------------|
| `perf_sample` | fps, frame_time_ms, main_ms, render_ms, gc_alloc_kb, mem_mb |
| `jank` | duration_ms, max_frame_ms, recent_gc, recent_events |
| `startup` | phase1_ms (app→unity), phase2_ms (unity→first frame), tti_ms |
| `scene_load` | scene_name, load_ms, activate_ms |
| `asset_load` | key, type, download_ms, decompress_ms, deserialize_ms, instantiate_ms, size_bytes |
| `http` | api_name, method, code, dns_ms, tcp_ms, tls_ms, ttfb_ms, download_ms, size_bytes, error |
| `exception` | fingerprint, message, stack, count |
| `crash` | type, stack, tombstone/minidump ref, last_breadcrumbs |

### Jank Definition
- A frame is slow when it exceeds max(1.5x, +8 ms) of the expected frame time (running average of normal frames, never below 1000/targetFrameRate)
- Jank = slow frames < 250 ms apart totalling > 50 ms (a single 150 ms frame is one jank); capped at 5 s, at most 20 per minute (APM-21)
- Not janks: the first frames after start/resume/focus, scene-load frames, frame-rate changes
- On jank: capture scene/level, last 10s GC count/alloc/peak frame time

## Metrics Focus Areas

### Performance
- FPS distribution (P50/P90/P95/P99), not just average
- Frame time breakdown: Main Thread, Render Thread, GPU (where available)

### Memory
- Mono/Managed heap, GC count/duration
- Native memory by type: Texture, AudioClip, Mesh
- OOM precursor detection (sustained growth + threshold alerts)

### Startup/Loading (minimum 3 phases)
1. App launch → Unity init
2. Unity init → first frame
3. First frame → TTI (time to interactive)

### Crash Context
- Stack trace + last N breadcrumb events (scene switches, asset loads, network calls, user actions summary)
- Last N seconds performance summary (frame time, GC)

## Development Phases

**Phase 1** (MVP): Crash/exception clustering, FPS/jank by scene, startup/scene load timing, basic alerts

**Phase 2**: Asset load segmentation (Addressables/AB), GC/memory curves, OOM warning, breadcrumb correlation

**Phase 3**: HTTP timing breakdown, failure clustering, trace_id linking to backend APM

## Privacy Requirements

- No precise location, contacts, or input content
- URL parameters stripped (template only)
- IP used only for coarse geo (server-side anonymization)
- user_id must be optional and disableable
- Data retention, two classes stored in separate tables:
  - Technical data (perf, exceptions, crashes, breadcrumbs, logs): 30 days detail, 1 year aggregated
  - Business/ops events: minimized detail up to 1 year — whitelisted fields only (event name, properties, anonymous player_id, version, channel, timestamps, whitelisted context keys); no stacks, logs, breadcrumbs, IP or fine-grained device info; deletable per player_id; collected only with user consent
- SDK must support data deletion/disable for compliance requests

## Project Management (Memory Flow)

This repo is registered in Memory Flow as project **APM** (OZX APM). Use the `memory-flow-pm` skill / `mf` CLI.

- Issue keys: `APM-<N>`; commit messages use `[APM-<N>] description`
- Close issues with `mf issue done APM-<N> --git HEAD`
- CI/CD (server): https://cicd.local.playquota.com/apps/36
- CI/CD (admin): https://cicd.local.playquota.com/apps/38
