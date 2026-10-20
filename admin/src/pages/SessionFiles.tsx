import { useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { formatDateTime, formatNumber } from '../utils/format'
import { sessionLink } from '../utils/timeline'
import { formatBytes } from '../components/SessionRecordings'
import {
  createDownloadLink,
  getToken,
  listSessionFiles,
  sessionFilesError,
  setToken,
} from '../services/sessionFiles'
import type { SessionFile, SessionFileFilter } from '../services/sessionFiles'

const filterKeys = ['git_commit', 'app_version', 'build', 'build_type', 'device_id', 'session_id', 'status'] as const

// When the recording was made: session.json's startedAt (AiTelemetry writes
// 7 fraction digits; cut to milliseconds), else the upload's time.
export function recordedAt(f: Pick<SessionFile, 'started_at' | 'created_at'>): string {
  const m = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(\.\d{1,3})?\d*(Z|[+-]\d{2}:\d{2})?$/.exec(f.started_at || '')
  if (m) {
    const t = Date.parse(m[1] + (m[2] || '') + (m[3] || 'Z'))
    if (!Number.isNaN(t)) return new Date(t).toISOString()
  }
  return f.created_at
}

// Session files (APM-13): the AiTelemetry session directories development
// builds uploaded, by git commit, build, device, time; a download is a zip of
// SessionFolder/<session>/ for scripts/ai_trace.py and verify-ticket.
export function SessionFiles() {
  const [search, setSearch] = useSearchParams()
  const [token, setTokenState] = useState(getToken())
  const [tokenInput, setTokenInput] = useState('')
  const [form, setForm] = useState<Record<string, string>>(() => {
    const f: Record<string, string> = {}
    for (const k of filterKeys) f[k] = search.get(k) || ''
    return f
  })
  const [downloadError, setDownloadError] = useState('')

  const filter: SessionFileFilter = { limit: 200 }
  for (const k of filterKeys) {
    const v = search.get(k)
    if (v) (filter as Record<string, string | number>)[k] = v
  }

  const { data, error, isLoading } = useQuery({
    queryKey: ['session-files', filter, token],
    queryFn: () => listSessionFiles(filter, token),
    enabled: !!token,
    retry: false,
  })

  const saveToken = (e: FormEvent) => {
    e.preventDefault()
    setToken(tokenInput.trim())
    setTokenState(tokenInput.trim())
    setTokenInput('')
  }

  const forget = () => {
    setToken('')
    setTokenState('')
  }

  const apply = (e: FormEvent) => {
    e.preventDefault()
    const params: Record<string, string> = {}
    for (const k of filterKeys) if (form[k].trim()) params[k] = form[k].trim()
    setSearch(params)
  }

  const download = async (f: SessionFile) => {
    setDownloadError('')
    try {
      const link = await createDownloadLink(f.upload_id, token)
      window.location.assign(link.url)
    } catch (err) {
      setDownloadError(sessionFilesError(err))
    }
  }

  const status = (error as { response?: { status?: number } } | null)?.response?.status
  return (
    <div className="space-y-6">
      <div className="bg-white rounded-lg shadow p-6 space-y-3">
        <h2 className="text-lg font-medium text-gray-900">Session files</h2>
        <p className="text-sm text-gray-600">
          AiTelemetry session directories recorded by development builds (<span className="font-mono">ApmSessionFiles.Upload</span>).
          A download is a zip of <span className="font-mono">SessionFolder/&lt;session&gt;/</span>: unzip it and run{' '}
          <span className="font-mono">python3 scripts/ai_trace.py &lt;session&gt;</span>. They contain full game data: listing
          and downloading need the session files token.
        </p>
        {!token || status === 401 ? (
          <form onSubmit={saveToken} className="flex items-end gap-2" aria-label="token">
            <label className="text-sm text-gray-700">
              Session files token
              <input
                type="password"
                autoComplete="off"
                value={tokenInput}
                onChange={(e) => setTokenInput(e.target.value)}
                className="block mt-1 border rounded px-2 py-1 text-sm w-80"
                placeholder="OZX_ADMIN_SESSIONFILES_TOKEN"
              />
            </label>
            <button type="submit" className="px-3 py-1 text-sm rounded bg-slate-800 text-white">
              Use for this tab
            </button>
          </form>
        ) : (
          <p className="text-xs text-gray-500">
            Token kept for this browser tab only.{' '}
            <button onClick={forget} className="text-blue-600 hover:underline">
              Forget it
            </button>
          </p>
        )}
        <form onSubmit={apply} className="grid grid-cols-2 md:grid-cols-4 gap-2 text-sm" aria-label="filter">
          {filterKeys.map((k) => (
            <label key={k} className="text-gray-700">
              {k.replace('_', ' ')}
              {k === 'build_type' || k === 'status' ? (
                <select
                  value={form[k]}
                  onChange={(e) => setForm({ ...form, [k]: e.target.value })}
                  className="block mt-1 border rounded px-2 py-1 w-full"
                >
                  <option value="">{k === 'status' ? 'complete' : 'any'}</option>
                  {(k === 'status' ? ['uploading', 'failed', 'all'] : ['dev', 'editor']).map((o) => (
                    <option key={o} value={o}>
                      {o}
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  value={form[k]}
                  onChange={(e) => setForm({ ...form, [k]: e.target.value })}
                  className="block mt-1 border rounded px-2 py-1 w-full font-mono"
                  placeholder={k === 'git_commit' ? 'prefix, e.g. 6116e2e' : ''}
                />
              )}
            </label>
          ))}
          <div className="flex items-end">
            <button type="submit" className="px-3 py-1 rounded bg-gray-100 hover:bg-gray-200">
              Search
            </button>
          </div>
        </form>
      </div>

      {token && (
        <div className="bg-white rounded-lg shadow p-6">
          {isLoading && <p className="text-sm text-gray-500">Loading...</p>}
          {error && <p className="text-sm text-red-600">{sessionFilesError(error)}</p>}
          {downloadError && <p className="text-sm text-red-600">{downloadError}</p>}
          {data && data.session_files.length === 0 && <p className="text-sm text-gray-500">No session files match.</p>}
          {data && data.session_files.length > 0 && (
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-gray-500 text-xs">
                  <th className="py-1">Uploaded</th>
                  <th className="py-1">Recording</th>
                  <th className="py-1">Commit</th>
                  <th className="py-1">Version</th>
                  <th className="py-1">Device</th>
                  <th className="py-1">Size</th>
                  <th className="py-1">Status</th>
                  <th className="py-1" />
                </tr>
              </thead>
              <tbody>
                {data.session_files.map((f) => (
                  <tr key={f.upload_id} className="border-t border-gray-100 align-top">
                    <td className="py-1 whitespace-nowrap">{formatDateTime(f.created_at)}</td>
                    <td className="py-1">
                      <span className="font-mono">{f.recording_name}</span>
                      {f.scene && <span className="text-xs text-gray-500"> · {f.scene}</span>}
                      {f.session_id && (
                        <div>
                          <Link to={sessionLink({ session_id: f.session_id, timestamp: recordedAt(f), session_ms: 0 })} className="text-xs text-blue-600 hover:underline">
                            Session time line
                          </Link>
                        </div>
                      )}
                    </td>
                    <td className="py-1 font-mono" title={f.git_commit}>
                      {f.git_commit ? f.git_commit.slice(0, 10) : '-'}
                    </td>
                    <td className="py-1">
                      {f.app_version || '-'}
                      {f.app_build ? ` (${f.app_build})` : ''} · {f.build_type}
                    </td>
                    <td className="py-1">
                      {f.device_model || '-'}
                      <div className="text-xs text-gray-500 font-mono">{f.device_id}</div>
                    </td>
                    <td className="py-1 whitespace-nowrap">
                      {formatBytes(f.size_bytes)}
                      <div className="text-xs text-gray-500">
                        {formatNumber(f.file_count)} files, {formatBytes(f.raw_bytes)}
                      </div>
                    </td>
                    <td className="py-1">
                      {f.status}
                      {f.error && <div className="text-xs text-red-600">{f.error}</div>}
                    </td>
                    <td className="py-1">
                      {f.status === 'complete' && (
                        <button
                          onClick={() => download(f)}
                          className="px-2 py-0.5 text-xs rounded bg-emerald-100 text-emerald-800 hover:bg-emerald-200"
                          title={`SHA-256 ${f.sha256}`}
                        >
                          Download
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  )
}
