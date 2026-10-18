import axios from 'axios'
import type {
  DashboardSummary,
  TimeSeriesResponse,
  DistributionResponse,
  CrashListResponse,
  CrashDetail,
  CrashOccurrence,
  ExceptionListResponse,
  FilterParams,
  PaginationParams,
  ContextKeysResponse,
  ContextValuesResponse,
  ContextDistribution,
  CustomEventsResponse,
  CustomMetricsResponse,
  CustomDistribution,
  CustomGroup,
  ExceptionSort,
  ExceptionDetail,
  ExceptionSample,
  ExceptionOccurrence,
  ExceptionOccurrencesResponse,
  ExceptionDistribution,
  ExceptionGroupBy,
  ParsedStack,
  SessionTimeline,
  SessionInfo,
  DeviceSessionsResponse,
  IssueKind,
  IssueState,
  IssueStatus,
} from '../types/api'

export const apiConfig = {
  baseURL: '/api',
  timeout: 30000,
  // Arrays as repeated keys (ctx=a&ctx=b), which is what the server reads;
  // axios' default would send ctx[]=a&ctx[]=b.
  paramsSerializer: { indexes: null },
}

const api = axios.create(apiConfig)

// Dashboard APIs
export async function getSummary(params: FilterParams): Promise<DashboardSummary> {
  const { data } = await api.get<DashboardSummary>('/summary', { params })
  if (!data) {
    return {
      time_range: { start: '', end: '' },
      total_sessions: 0,
      total_events: 0,
      crash_count: 0,
      crash_rate: 0,
      exception_count: 0,
      jank_count: 0,
      avg_fps: 0,
      avg_startup_ms: 0,
      top_versions: [],
      top_platforms: [],
    }
  }
  return {
    ...data,
    top_versions: data.top_versions || [],
    top_platforms: data.top_platforms || [],
  }
}

export async function getTimeSeries(
  metric: string,
  params: FilterParams
): Promise<TimeSeriesResponse> {
  const { data } = await api.get<TimeSeriesResponse>('/timeseries', {
    params: { metric, ...params },
  })
  if (!data) {
    return { metric, data: [] }
  }
  return { ...data, data: data.data || [] }
}

export async function getDistribution(
  metric: string,
  params: FilterParams & { scene?: string }
): Promise<DistributionResponse> {
  const { data } = await api.get<DistributionResponse>('/distribution', {
    params: { metric, ...params },
  })
  if (!data) {
    return { metric, buckets: [], p50: 0, p90: 0, p95: 0, p99: 0 }
  }
  return { ...data, buckets: data.buckets || [] }
}

export async function getAppVersions(): Promise<string[]> {
  const { data } = await api.get<{ versions: string[] }>('/versions')
  return data?.versions || []
}

export async function getScenes(appVersion?: string): Promise<string[]> {
  const { data } = await api.get<{ scenes: string[] }>('/scenes', {
    params: { app_version: appVersion },
  })
  return data?.scenes || []
}

// Crash APIs
export async function getCrashes(
  params: FilterParams & PaginationParams
): Promise<CrashListResponse> {
  const { data } = await api.get<CrashListResponse>('/crashes', { params })
  if (!data) {
    return { crashes: [], total_count: 0, page: 1, page_size: 20 }
  }
  return { ...data, crashes: data.crashes || [] }
}

export async function getCrashDetail(
  fingerprint: string,
  params: FilterParams
): Promise<CrashDetail> {
  const { data } = await api.get<CrashDetail>('/crashes/detail', {
    params: { fingerprint, ...params },
  })
  if (!data) {
    throw new Error('Crash detail not found')
  }
  return {
    ...data,
    occurrences: (data.occurrences || []).map(normalizeCrashOccurrence),
    version_distribution: data.version_distribution || [],
    device_distribution: data.device_distribution || [],
    os_distribution: data.os_distribution || [],
    sample: data.sample ? normalizeCrashOccurrence(data.sample) : null,
    symbols: data.symbols || [],
  }
}

// Missing lists and maps (older servers, empty Go slices) read as empty.
export function normalizeCrashOccurrence(o: CrashOccurrence): CrashOccurrence {
  return {
    ...o,
    breadcrumbs: o.breadcrumbs || [],
    context: o.context || {},
    details: o.details || {},
    threads: (o.threads || []).map((t) => ({ ...t, frames: t.frames || [] })),
  }
}

// Exception APIs
// sort (APM-11): count (server default), sessions, first_seen, last_seen.
// first_error (APM-3): only occurrences that were their session's first error.
// status (APM-8): resolved, unresolved or all (default).
export async function getExceptions(
  params: FilterParams & PaginationParams & { sort?: ExceptionSort; first_error?: string; status?: IssueStatus }
): Promise<ExceptionListResponse> {
  const { data } = await api.get<ExceptionListResponse>('/exceptions', { params })
  if (!data) {
    return { exceptions: [], total_count: 0, page: 1, page_size: 20 }
  }
  return { ...data, exceptions: data.exceptions || [] }
}

// Issue state (APM-8). A change sends X-Apm-Admin, which the server requires
// (other origins cannot send it).
export async function getIssueState(kind: IssueKind, fingerprint: string): Promise<IssueState> {
  const { data } = await api.get<IssueState>('/issues/state', { params: { kind, fingerprint } })
  return data
}

export interface IssueStateChange {
  kind: IssueKind
  fingerprint: string
  status: IssueStatus
  resolved_in_version?: string
  comment?: string
}

export async function setIssueState(change: IssueStateChange): Promise<IssueState> {
  const { data } = await api.post<IssueState>('/issues/state', change, {
    headers: { 'Content-Type': 'application/json', 'X-Apm-Admin': '1' },
  })
  return data
}

// Fields an old server or an old SDK's row may lack become empty values, so
// the pages never have to check for undefined.
export function normalizeOccurrence(o: Partial<ExceptionOccurrence>): ExceptionOccurrence {
  return {
    timestamp: o.timestamp || '',
    event_id: o.event_id || '',
    count: o.count || 0,
    app_version: o.app_version || '',
    build_type: o.build_type || '',
    platform: o.platform || '',
    device_model: o.device_model || '',
    os_version: o.os_version || '',
    scene: o.scene || '',
    session_id: o.session_id || '',
    device_id: o.device_id || '',
    context: o.context || {},
    breadcrumbs: o.breadcrumbs || [],
    error_seq: o.error_seq || 0,
    session_ms: o.session_ms || 0,
    unhandled: o.unhandled || '',
    ctx_merged: !!o.ctx_merged,
  }
}

function normalizeParsed(p: Partial<ParsedStack> | undefined): ParsedStack {
  return {
    kind: p?.kind || '',
    wrapped: !!p?.wrapped,
    chain: (p?.chain || []).map((e) => ({ type: e.type || '', message: e.message || '', frames: e.frames || [] })),
    log_frames: p?.log_frames || [],
    message_template: p?.message_template || '',
    throw_site: p?.throw_site || '',
    call_site: p?.call_site || '',
    fingerprint: p?.fingerprint || '',
    fingerprint_version: p?.fingerprint_version || 0,
  }
}

// APM-4/APM-11: one exception group. Rejects with the axios error (404 when
// nothing of the group matches the filter).
export async function getExceptionDetail(fingerprint: string, params: FilterParams): Promise<ExceptionDetail> {
  const { data } = await api.get<ExceptionDetail>('/exceptions/detail', {
    params: { fingerprint, ...params },
  })
  if (!data) {
    throw new Error('Exception detail not found')
  }
  const s: Partial<ExceptionSample> = data.sample || {}
  return {
    ...data,
    message: data.message || '',
    first_version: data.first_version || '',
    last_version: data.last_version || '',
    sample: {
      timestamp: s.timestamp || '',
      event_id: s.event_id || '',
      log_type: s.log_type || '',
      message: s.message || '',
      stack: s.stack || '',
      embedded_exception: s.embedded_exception || '',
      client_fingerprint: s.client_fingerprint || '',
      fingerprint_version: s.fingerprint_version || 0,
      inner_exceptions: s.inner_exceptions || [],
      parsed: normalizeParsed(s.parsed),
    },
    occurrences: (data.occurrences || []).map(normalizeOccurrence),
    version_distribution: data.version_distribution || [],
    device_distribution: data.device_distribution || [],
    trend: { interval_seconds: data.trend?.interval_seconds || 0, points: data.trend?.points || [] },
  }
}

export async function getExceptionOccurrences(
  fingerprint: string,
  params: FilterParams & PaginationParams
): Promise<ExceptionOccurrencesResponse> {
  const { data } = await api.get<ExceptionOccurrencesResponse>('/exceptions/occurrences', {
    params: { fingerprint, ...params },
  })
  return {
    fingerprint,
    occurrences: (data?.occurrences || []).map(normalizeOccurrence),
    total_count: data?.total_count || 0,
    page: data?.page || 1,
    page_size: data?.page_size || params.page_size || 10,
  }
}

export async function getExceptionDistribution(
  fingerprint: string,
  group: ExceptionGroupBy,
  params: FilterParams
): Promise<ExceptionDistribution> {
  const { data } = await api.get<ExceptionDistribution>('/exceptions/distribution', {
    params: { fingerprint, [group.kind]: group.key, ...params },
  })
  return {
    fingerprint,
    group_by: data?.group_by || '',
    total: data?.total || 0,
    merged: data?.merged || 0,
    truncated: !!data?.truncated,
    rows: data?.rows || [],
  }
}

// Session time line (APM-15). at: the moment to anchor on (an occurrence).
export interface SessionTimelineParams {
  session_id: string
  at?: string
  start_time?: string
  end_time?: string
  perf?: string
}

export async function getSessionTimeline(params: SessionTimelineParams): Promise<SessionTimeline> {
  const { data } = await api.get<SessionTimeline>('/sessions/timeline', { params })
  const s: Partial<SessionInfo> = data?.session || {}
  return {
    session_id: data?.session_id || params.session_id,
    start_time: data?.start_time || '',
    end_time: data?.end_time || '',
    found: !!data?.found,
    session: {
      session_id: s.session_id || params.session_id,
      first_seen: s.first_seen || '',
      last_seen: s.last_seen || '',
      started_at: s.started_at,
      device_id: s.device_id || '',
      app_version: s.app_version || '',
      platform: s.platform || '',
      device_model: s.device_model || '',
      os_version: s.os_version || '',
      build_type: s.build_type || '',
      rows: s.rows || 0,
    },
    items: data?.items || [],
    counts: data?.counts || {},
    truncated: data?.truncated || [],
    duplicate_breadcrumbs: data?.duplicate_breadcrumbs || 0,
    perf: data?.perf || [],
    perf_interval_seconds: data?.perf_interval_seconds || 0,
  }
}

export async function getDeviceSessions(
  deviceId: string,
  params: { start_time?: string; end_time?: string }
): Promise<DeviceSessionsResponse> {
  const { data } = await api.get<DeviceSessionsResponse>('/sessions', {
    params: { device_id: deviceId, ...params },
  })
  return {
    device_id: deviceId,
    start_time: data?.start_time || '',
    end_time: data?.end_time || '',
    sessions: data?.sessions || [],
    truncated: !!data?.truncated,
  }
}

// Game context APIs (APM-5). source: perf, janks, startups, scene_loads,
// exceptions, crashes or all (default).
export async function getContextKeys(
  params: FilterParams & { source?: string }
): Promise<ContextKeysResponse> {
  const { data } = await api.get<ContextKeysResponse>('/context/keys', { params })
  return { keys: data?.keys || [] }
}

export async function getContextValues(
  key: string,
  params: FilterParams & { source?: string; limit?: number }
): Promise<ContextValuesResponse> {
  const { data } = await api.get<ContextValuesResponse>('/context/values', {
    params: { key, ...params },
  })
  return { key, values: data?.values || [] }
}

export async function getContextDistribution(
  key: string,
  metric: string,
  params: FilterParams & { fingerprint?: string }
): Promise<ContextDistribution> {
  const { data } = await api.get<ContextDistribution>('/context/distribution', {
    params: { key, metric, ...params },
  })
  return {
    key,
    metric,
    unit: data?.unit || '',
    total: data?.total || 0,
    truncated: !!data?.truncated,
    rows: data?.rows || [],
  }
}

// Custom events and metrics (APM-6)
export async function getCustomEvents(params: FilterParams): Promise<CustomEventsResponse> {
  const { data } = await api.get<CustomEventsResponse>('/custom/events', { params })
  return { events: data?.events || [], truncated: !!data?.truncated }
}

export async function getCustomMetrics(params: FilterParams): Promise<CustomMetricsResponse> {
  const { data } = await api.get<CustomMetricsResponse>('/custom/metrics', { params })
  return { metrics: data?.metrics || [], truncated: !!data?.truncated }
}

// group: key=<context key>, prop=<property key> (events), by=<column>.
export async function getCustomDistribution(
  kind: 'events' | 'metrics',
  name: string,
  group: CustomGroup,
  params: FilterParams
): Promise<CustomDistribution> {
  const { data } = await api.get<CustomDistribution>(`/custom/${kind}/distribution`, {
    params: { name, [group.kind]: group.key, ...params },
  })
  return {
    name,
    group_by: data?.group_by || '',
    unit: data?.unit || '',
    total: data?.total || 0,
    merged: data?.merged || 0,
    truncated: !!data?.truncated,
    rows: data?.rows || [],
  }
}
