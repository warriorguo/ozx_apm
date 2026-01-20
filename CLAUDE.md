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
- Single frame > 50ms = jank event
- Consecutive frames > 33ms = sustained jank
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
- Data retention: 30 days detail, 1 year aggregated
- SDK must support data deletion/disable for compliance requests
