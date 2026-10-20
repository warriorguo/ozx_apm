import axios from 'axios'
import type { AxiosError } from 'axios'
// Own instance (same settings as services/api): tests that mock services/api leave this one alone.

// Session files (APM-13): the AiTelemetry session directories development
// builds upload. Listing and downloading need the admin token
// (OZX_ADMIN_SESSIONFILES_TOKEN); the admin UI has no login, so the token is
// asked for and kept in sessionStorage (this browser tab only, gone when the
// tab closes), and sent as a bearer header. A download asks the server for a
// link signed for one upload and five minutes, which the browser then opens
// (a plain navigation cannot carry the header).

export interface SessionFile {
  upload_id: string
  status: 'uploading' | 'complete' | 'failed'
  created_at: string
  updated_at: string
  expires_at: string
  device_id: string
  session_id: string
  recording_name: string
  app_version: string
  app_build: string
  build_type: string
  platform: string
  device_model: string
  os_version: string
  unity_version: string
  sdk_version: string
  git_commit: string
  started_at: string
  scene: string
  data_source: string
  data_pack_version: string
  data_pack_sha256: string
  size_bytes: number
  part_bytes: number
  part_count: number
  sha256: string
  file_count: number
  raw_bytes: number
  session_json?: string
  error?: string
}

export interface SessionFilesResponse {
  session_files: SessionFile[]
  count: number
}

export interface SessionFileFilter {
  git_commit?: string
  app_version?: string
  build?: string
  build_type?: string
  device_id?: string
  session_id?: string
  recording?: string
  status?: string
  start_time?: string
  end_time?: string
  limit?: number
}

export interface SessionFileAvailability {
  session_id: string
  upload_id: string
  size_bytes: number
  created_at: string
}

export interface DownloadLink {
  url: string
  expires_at: string
  filename: string
}

const TOKEN_KEY = 'ozx_apm_sessionfiles_token'

const api = axios.create({ baseURL: '/api', timeout: 30000, paramsSerializer: { indexes: null } })

// The token of this tab (sessionStorage may be unavailable: then nothing is kept).
export function getToken(): string {
  try {
    return window.sessionStorage.getItem(TOKEN_KEY) || ''
  } catch {
    return ''
  }
}

export function setToken(token: string) {
  try {
    if (token) window.sessionStorage.setItem(TOKEN_KEY, token)
    else window.sessionStorage.removeItem(TOKEN_KEY)
  } catch {
    // not kept
  }
}

function auth(token: string) {
  return { Authorization: `Bearer ${token}` }
}

function clean(filter: SessionFileFilter): Record<string, string | number> {
  const out: Record<string, string | number> = {}
  for (const [k, v] of Object.entries(filter)) {
    if (v !== undefined && v !== '') out[k] = typeof v === 'string' ? v.trim() : v
  }
  return out
}

export async function listSessionFiles(filter: SessionFileFilter, token: string): Promise<SessionFilesResponse> {
  const { data } = await api.get<SessionFilesResponse>('/session-files', { params: clean(filter), headers: auth(token) })
  return { session_files: data?.session_files ?? [], count: data?.count ?? 0 }
}

export async function getSessionFile(uploadId: string, token: string): Promise<SessionFile> {
  const { data } = await api.get<SessionFile>('/session-files/detail', { params: { upload_id: uploadId }, headers: auth(token) })
  return data
}

export async function createDownloadLink(uploadId: string, token: string): Promise<DownloadLink> {
  const { data } = await api.post<DownloadLink>('/session-files/link', { upload_id: uploadId }, { headers: auth(token) })
  return data
}

// Which of the sessions have a recording (no token needed: ids and sizes only).
export async function getSessionFileAvailability(sessionIds: string[]): Promise<SessionFileAvailability[]> {
  if (sessionIds.length === 0) return []
  const { data } = await api.get<{ recordings?: SessionFileAvailability[] }>('/session-files/available', {
    params: { session_id: sessionIds },
  })
  return data?.recordings ?? []
}

// A readable reason for a failed request.
export function sessionFilesError(err: unknown): string {
  const e = err as AxiosError<{ error?: string }>
  const status = e?.response?.status ?? 0
  const message = e?.response?.data?.error
  if (status === 401) return 'The token is wrong. Enter the session files token (OZX_ADMIN_SESSIONFILES_TOKEN).'
  if (status === 403) return message || 'The session files API is disabled on this server (no token configured).'
  if (status === 503) return message || 'Session files are not configured on this server (object store).'
  if (status === 410) return 'The recording has expired.'
  return message || (e?.message ?? 'Request failed')
}

export function sessionFilesLink(params: Record<string, string>): string {
  return '/session-files?' + new URLSearchParams(params).toString()
}
