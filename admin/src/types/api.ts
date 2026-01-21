// Dashboard types
export interface TimeRange {
  start: string
  end: string
}

export interface VersionStats {
  version: string
  session_count: number
  crash_count: number
  crash_rate: number
}

export interface PlatformStats {
  platform: string
  session_count: number
  avg_fps: number
}

export interface DashboardSummary {
  time_range: TimeRange
  total_sessions: number
  total_events: number
  crash_count: number
  crash_rate: number
  exception_count: number
  jank_count: number
  avg_fps: number
  avg_startup_ms: number
  top_versions: VersionStats[]
  top_platforms: PlatformStats[]
}

export interface TimeSeriesPoint {
  timestamp: string
  value: number
}

export interface TimeSeriesResponse {
  metric: string
  data: TimeSeriesPoint[]
}

export interface DistributionBucket {
  bucket: string
  count: number
  pct: number
}

export interface DistributionResponse {
  metric: string
  buckets: DistributionBucket[]
  p50: number
  p90: number
  p95: number
  p99: number
}

// Crash types
export interface CrashGroup {
  fingerprint: string
  crash_type: string
  sample_message: string
  count: number
  session_count: number
  first_seen: string
  last_seen: string
  affected_versions: string[]
  top_devices: string[]
}

export interface CrashListResponse {
  crashes: CrashGroup[]
  total_count: number
  page: number
  page_size: number
}

export interface CrashOccurrence {
  timestamp: string
  app_version: string
  platform: string
  device_model: string
  os_version: string
  scene: string
  breadcrumbs: string[]
}

export interface VersionDist {
  version: string
  count: number
}

export interface DeviceDist {
  device: string
  count: number
}

export interface OSDist {
  os: string
  count: number
}

export interface CrashDetail {
  fingerprint: string
  crash_type: string
  stack: string
  count: number
  session_count: number
  first_seen: string
  last_seen: string
  occurrences: CrashOccurrence[]
  version_distribution: VersionDist[]
  device_distribution: DeviceDist[]
  os_distribution: OSDist[]
}

// Exception types
export interface ExceptionGroup {
  fingerprint: string
  message: string
  count: number
  session_count: number
  first_seen: string
  last_seen: string
}

export interface ExceptionListResponse {
  exceptions: ExceptionGroup[]
  total_count: number
  page: number
  page_size: number
}

// Query params
export interface TimeRangeParams {
  start_time?: string
  end_time?: string
}

export interface FilterParams extends TimeRangeParams {
  app_version?: string
  platform?: string
}

export interface PaginationParams {
  page?: number
  page_size?: number
}
