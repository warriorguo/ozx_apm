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
// APM-8: the state of an exception or crash group. Absent from older servers
// and when the server could not read the states (then nothing is shown).
export type IssueStatus = 'unresolved' | 'resolved'
export type IssueKind = 'exception' | 'crash'

export interface IssueStatusInfo {
  status?: IssueStatus
  resolved_in_version?: string
  resolved_at?: string
  regressed_in_version?: string
}

export interface IssueState {
  kind: IssueKind
  fingerprint: string
  status: IssueStatus
  resolved_in_version: string
  last_seen_version: string
  resolved_at?: string
  regressed_in_version?: string
  regressed_at?: string
  updated_at: string
  updated_by: string
  comment?: string
}

export interface CrashGroup extends IssueStatusInfo {
  fingerprint: string
  crash_type: string
  // The latest crash's summary (APM-7), e.g. "SIGSEGV (SEGV_MAPERR) at 0x8 in Game.Load (libil2cpp.so)"
  sample_message: string
  count: number
  session_count: number
  first_seen: string
  last_seen: string
  affected_versions: string[]
  top_devices: string[]
  // APM-7; absent in responses from servers before it
  exit_reason?: string
  signal?: string
  crash_site?: string
  // The latest crash's first in-app frame, symbolicated when it is read: "Method (File.cs:12)"
  top_frame?: string
  foreground?: number
  symbolicated?: boolean
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
  // APM-5; absent in responses from servers before it
  build_type?: string
  context?: Record<string, string>
  // APM-7; absent in responses from servers before it
  event_id?: string
  session_id?: string
  device_id?: string
  crash_type?: string
  summary?: string
  exit_reason?: string
  exit_status?: number
  exit_description?: string
  importance?: number
  pss_kb?: number
  rss_kb?: number
  signal?: string
  signal_code?: string
  fault_addr?: string
  abort_message?: string
  abi?: string
  session_ms?: number
  foreground?: boolean
  details?: Record<string, string>
  // The sample only: the stack as sent and its threads, symbolicated
  stack?: string
  threads?: CrashThread[]
}

// A crash stack's thread (APM-7): the crashed one first, then UnityMain and main.
export interface CrashThread {
  name: string
  tid: number
  crashed: boolean
  frames: CrashFrame[]
  omitted: number
}

// function: the C# method (or C++ function) from the uploaded symbols when
// symbolicated, else what the device reported; empty: an address only.
export interface CrashFrame {
  index: number
  native: boolean
  pc?: string
  module?: string
  build_id?: string
  function?: string
  offset?: number
  file?: string
  line?: number
  in_app: boolean
  symbolicated: boolean
  symbols_by?: string
  raw: string
}

export interface CrashSymbolStatus {
  module: string
  build_id: string
  found: boolean
  upload_id?: string
  uploaded_at?: string
  line_map: boolean
  method_map: boolean
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

export interface CrashDetail extends IssueStatusInfo {
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
  // APM-7: the latest crash with its stack symbolicated; null without one or from older servers
  sample?: CrashOccurrence | null
  fingerprint_version?: number
  symbols?: CrashSymbolStatus[]
}

// Exception types
export interface ExceptionGroup extends IssueStatusInfo {
  fingerprint: string
  message: string
  count: number
  session_count: number
  first_seen: string
  last_seen: string
  // APM-4: the server's grouping. Empty (version 0) for groups stored before
  // it, and absent from older servers.
  fingerprint_version?: number
  exception_type?: string
  message_template?: string
  throw_site?: string
  call_site?: string
  // APM-3: sessions where this group was the first error, and occurrences no
  // code caught. Absent from older servers, 0 for rows of older SDKs.
  first_error_sessions?: number
  unhandled_count?: number
  // APM-11: sessions whose rows carry an error sequence (SDK with APM-3);
  // first_error_sessions / sessions_with_seq = share of sessions in which
  // this group was the first error.
  sessions_with_seq?: number
}

export type ExceptionSort = 'count' | 'sessions' | 'first_seen' | 'last_seen'

// The exception detail (APM-4 server, APM-11 page). Everything that older
// servers or older SDK rows may lack is optional.
export interface ParsedFrame {
  method: string
  file?: string
  line?: number
  in_app: boolean
  raw: string
}

export interface ParsedException {
  type: string
  message: string
  frames: ParsedFrame[]
}

export interface ParsedStack {
  kind: string // exception, error, assert
  wrapped: boolean // the exception was embedded in an error/assert log
  chain: ParsedException[] // outermost first
  log_frames: ParsedFrame[] // Unity's stack of the log call
  message_template: string
  throw_site: string
  call_site: string
  fingerprint: string
  fingerprint_version: number
}

export interface ExceptionSample {
  timestamp: string
  event_id: string
  log_type: string
  message: string
  stack: string
  embedded_exception: string
  client_fingerprint: string
  fingerprint_version: number
  inner_exceptions: string[]
  parsed: ParsedStack
}

export interface ExceptionOccurrence {
  timestamp: string
  event_id: string
  count: number
  app_version: string
  build_type: string
  platform: string
  device_model: string
  os_version: string
  scene: string
  session_id: string
  device_id: string
  context: Record<string, string>
  breadcrumbs: string[]
  error_seq: number // 0: unknown (SDK before APM-3)
  session_ms: number
  unhandled: string // '', domain, domain_terminating, task
  ctx_merged: boolean
}

export interface ExceptionTrendPoint {
  timestamp: string
  count: number
  sessions: number
}

export interface ExceptionDetail extends ExceptionGroup {
  first_version: string
  last_version: string
  sample: ExceptionSample
  occurrences: ExceptionOccurrence[]
  version_distribution: VersionDist[]
  device_distribution: DeviceDist[]
  trend: { interval_seconds: number; points: ExceptionTrendPoint[] }
}

export interface ExceptionOccurrencesResponse {
  fingerprint: string
  occurrences: ExceptionOccurrence[]
  total_count: number
  page: number
  page_size: number
}

export type ExceptionColumn = 'app_version' | 'build_type' | 'device_model' | 'platform' | 'os_version'

// by a column, or by a game context key
export type ExceptionGroupBy = { kind: 'by'; key: ExceptionColumn } | { kind: 'key'; key: string }

export interface ExceptionDistributionRow {
  value: string
  count: number
  sessions: number
}

export interface ExceptionDistribution {
  fingerprint: string
  group_by: string
  total: number
  merged: number // occurrences whose value of the key is unknown (merged across contexts)
  truncated: boolean
  rows: ExceptionDistributionRow[]
}

// Session time line (APM-15)
export type TimelineType =
  | 'custom_event'
  | 'custom_metric'
  | 'exception'
  | 'crash'
  | 'jank'
  | 'scene_load'
  | 'startup'
  | 'run'
  | 'context'
  | 'breadcrumb'

export type BreadcrumbKind = 'event' | 'scene' | 'warning' | 'error' | 'assert' | 'exception' | 'text'

export interface ContextChange {
  key: string
  from: string
  to: string
}

// One item; only the fields of its type are present.
export interface TimelineItem {
  time: string
  type: TimelineType
  event_id?: string
  seq?: number
  scene?: string
  name?: string
  aggregated?: boolean
  props?: Record<string, string>
  unit?: string
  sum?: number
  min?: number
  max?: number
  window_ms?: number
  count?: number
  ctx_merged?: boolean
  context?: Record<string, string>
  fingerprint?: string
  message?: string
  exception_type?: string
  log_type?: string
  throw_site?: string
  call_site?: string
  error_seq?: number
  session_ms?: number
  unhandled?: string
  crash_type?: string
  duration_ms?: number
  max_frame_ms?: number
  recent_gc_count?: number
  recent_gc_alloc_kb?: number
  recent_events?: string[]
  scene_name?: string
  load_ms?: number
  activate_ms?: number
  phase1_ms?: number
  phase2_ms?: number
  tti_ms?: number
  changes?: ContextChange[]
  source?: string
  kind?: BreadcrumbKind
  text?: string
  repeats?: number
  repeat_until?: string
  time_unknown?: boolean
  // run (APM-9): a game run that ended (duration_ms: its length)
  run_id?: string
  end_reason?: string
  result?: string
  peak_rss_mb?: number
  peak_alloc_mb?: number
  peak_mem_mb?: number
}

export interface SessionInfo {
  session_id: string
  first_seen: string
  last_seen: string
  started_at?: string
  device_id: string
  app_version: string
  platform: string
  device_model: string
  os_version: string
  build_type: string
  rows: number
}

export interface PerfPoint {
  time: string
  samples: number
  fps_avg: number
  fps_min: number
  frame_ms_max: number
  mem_mb_max: number
}

export interface SessionTimeline {
  session_id: string
  start_time: string
  end_time: string
  found: boolean
  session: SessionInfo
  items: TimelineItem[]
  counts: Record<string, number>
  truncated: string[]
  duplicate_breadcrumbs: number
  perf: PerfPoint[]
  perf_interval_seconds: number
}

export interface SessionSummary {
  session_id: string
  first_seen: string
  last_seen: string
  rows: number
  exceptions: number
  crashes: number
  app_version: string
  platform: string
  device_model: string
  build_type: string
}

export interface DeviceSessionsResponse {
  device_id: string
  start_time: string
  end_time: string
  sessions: SessionSummary[]
  truncated: boolean
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
  // APM-5. build_type: release (server default; includes events from SDKs
  // without a build type), dev, editor, unknown, all.
  build_type?: string
  // "key:value"; the same key repeated is an OR, different keys an AND.
  ctx?: string[]
}

// Game context (APM-5)
export type BuildTypeFilter = 'release' | 'dev' | 'editor' | 'unknown' | 'all'

export interface ContextFilter {
  key: string
  value: string // '' matches events without the key
}

export interface ContextKeyStat {
  key: string
  count: number
}

export interface ContextKeysResponse {
  keys: ContextKeyStat[]
}

export interface ContextValueStat {
  value: string
  count: number
}

export interface ContextValuesResponse {
  key: string
  values: ContextValueStat[]
}

export type ContextMetric =
  | 'fps'
  | 'frame_time'
  | 'janks'
  | 'jank_rate'
  | 'startup'
  | 'scene_load'
  | 'exceptions'
  | 'crashes'

export interface ContextDistributionRow {
  value: string // '' = key not set
  count: number
  sessions: number
  // APM-9; absent from older servers
  p05?: number
  p50: number
  p90?: number
  p95: number
  p99?: number
  // jank_rate (APM-9): count = janks, minutes of play, janks per minute
  minutes?: number
  rate?: number
}

export interface ContextDistribution {
  key: string
  metric: string
  unit: string // 'fps', 'ms', or '' for counts only
  total: number
  truncated: boolean
  rows: ContextDistributionRow[]
  // APM-3 (exceptions): occurrences whose value of the key is unknown because
  // the SDK merged them across contexts; not in rows.
  merged?: number
  // APM-9: 'count' when numeric values were grouped into count ranges
  bucket?: string
}

export interface PaginationParams {
  page?: number
  page_size?: number
}

// Custom events and metrics (APM-6)
export interface CustomEventStat {
  name: string
  count: number // calls (sum of count)
  sessions: number
  devices: number
  last_seen: string
}

export interface CustomEventsResponse {
  events: CustomEventStat[]
  truncated: boolean
}

// One metric, or one group of a distribution. Percentiles: exact rank,
// value within 2% (the SDK's histogram buckets).
export interface CustomMetricStat {
  name?: string
  value: string
  unit?: string
  count: number
  sessions: number
  avg: number
  min: number
  max: number
  p50: number
  p90: number
  p95: number
  p99: number
}

export interface CustomMetricsResponse {
  metrics: CustomMetricStat[]
  truncated: boolean
}

export type CustomColumn = 'app_version' | 'platform' | 'device_model' | 'os_version' | 'build_type'

// How a distribution groups: a game context key, an event property, or a column.
export type CustomGroup =
  | { kind: 'key'; key: string }
  | { kind: 'prop'; key: string }
  | { kind: 'by'; key: CustomColumn }

export interface CustomDistribution {
  name: string
  group_by: string
  unit: string
  total: number
  // Calls/samples whose value is unknown (merged across contexts by the SDK,
  // or aggregated events without properties); not in rows.
  merged: number
  truncated: boolean
  rows: CustomMetricStat[]
}

// APM-9: memory, session health, funnels

// How an analysis groups: a column or a game context key.
export type AnalysisColumn = 'app_version' | 'build_type' | 'device_model' | 'platform' | 'os_version' | 'ram_class'
export type AnalysisGroup = { kind: 'by'; key: AnalysisColumn } | { kind: 'key'; key: string }

export type MemoryFigureName = 'mem_mb' | 'mem_mono_heap_mb' | 'mem_alloc_mb' | 'mem_reserved_mb' | 'mem_gfx_mb' | 'mem_rss_mb'

// One perf sample memory column (MB) over the samples that have it (> 0).
export interface MemoryFigure {
  rows: number
  p50: number
  p95: number
  max: number
}

export interface MemoryBreakdownRow {
  value: string
  samples: number
  sessions: number
  figures: Partial<Record<MemoryFigureName, MemoryFigure>>
}

export interface MemoryBreakdownResponse {
  group_by: string
  rows: MemoryBreakdownRow[]
  truncated: boolean
}

export type RunPeakMetric = 'peak_rss' | 'peak_alloc' | 'peak_reserved' | 'peak_gfx' | 'peak_mem' | 'peak_mono_heap'

export interface RunPeakRow {
  value: string
  runs: number
  with_value: number
  sessions: number
  devices: number
  crashed_runs: number
  oom_runs: number
  killed_runs: number
  p50: number
  p90: number
  p99: number
  max: number
  avg_duration_ms: number
  avg_growth_mb: number
}

// [low, high) MB; high 0 = open-ended
export interface RunPeakBucket {
  low: number
  high: number
  runs: number
  oom: number
}

export interface RunPeaksResponse {
  metric: string
  group_by: string
  rows: RunPeakRow[]
  buckets: RunPeakBucket[]
  truncated: boolean
}

export type CrashTypeName = 'native' | 'java' | 'oom' | 'killed' | 'anr' | 'unknown'

// Rates are fractions (0.993 = 99.3%).
export interface SessionHealthRow {
  value: string
  build_type: string
  sessions: number
  users: number
  crashed_sessions: number
  crashed_users: number
  crash_free_sessions: number
  crash_free_users: number
  anr_sessions: number
  anr_rate: number
  exception_sessions: number
  exceptions: number
  exception_session_rate: number
  exceptions_per_session: number
  crashes_by_type: Record<string, number>
}

export interface SessionHealthResponse {
  group_by: string
  crash_types: string[]
  rows: SessionHealthRow[]
  truncated: boolean
}

export interface FunnelStep {
  name: string
  props?: Record<string, string>
}

export interface FunnelStepResult {
  name: string
  props?: Record<string, string>
  sessions: number
  conversion: number
  from_previous: number
}

export interface FunnelGroup {
  value: string
  steps: FunnelStepResult[]
}

export type FunnelSplit = '' | 'app_version' | 'build_type' | 'platform'

export interface FunnelResponse {
  start_time: string
  end_time: string
  window_seconds: number
  group_by: string
  steps: FunnelStep[]
  groups: FunnelGroup[]
  truncated: boolean
}
