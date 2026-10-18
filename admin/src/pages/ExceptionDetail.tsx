import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { GlobalFilterBar } from '../components/FilterBar'
import { Pagination } from '../components/Pagination'
import { TimeSeriesChart } from '../components/TimeSeriesChart'
import { BreadcrumbList } from '../components/BreadcrumbList'
import { IssueStatusControl } from '../components/IssueStatus'
import { useFilters } from '../hooks/useFilters'
import {
  useContextKeys,
  useExceptionDetail,
  useExceptionDistribution,
  useExceptionOccurrences,
  isValidContextKey,
} from '../hooks/useApi'
import { formatDateTime, formatNumber } from '../utils/format'
import { deviceSessionsLink, fillTrend, formatSessionMs, sessionLink } from '../utils/timeline'
import type {
  ExceptionColumn,
  ExceptionDetail as Detail,
  ExceptionGroupBy,
  ExceptionOccurrence,
  ParsedFrame,
} from '../types/api'

const columns: { value: ExceptionColumn; label: string }[] = [
  { value: 'app_version', label: 'Version' },
  { value: 'build_type', label: 'Build type' },
  { value: 'device_model', label: 'Device' },
  { value: 'platform', label: 'Platform' },
  { value: 'os_version', label: 'OS version' },
]

function httpStatus(err: unknown): number {
  const r = (err as { response?: { status?: number } } | null)?.response
  return r?.status ?? 0
}

function percent(part: number, whole: number): string {
  if (!whole) return '-'
  return `${Math.round((part / whole) * 100)}%`
}

// The exception detail page (APM-11): one group (fingerprint) with its stack,
// trend, distribution and occurrences. All filters of the filter bar apply.
export function ExceptionDetail() {
  const [search] = useSearchParams()
  const fingerprint = search.get('fingerprint') || ''
  const { queryParams } = useFilters()
  const { data, isLoading, isError, error } = useExceptionDetail(fingerprint, queryParams)

  if (!fingerprint) {
    return (
      <div>
        <BackLink />
        <p className="text-gray-500">No exception group selected.</p>
      </div>
    )
  }

  return (
    <div>
      <BackLink />
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Exception</h1>
      <GlobalFilterBar />
      {isLoading ? (
        <div className="bg-white rounded-lg shadow p-8 text-center text-gray-500">Loading...</div>
      ) : isError || !data ? (
        <div className="bg-white rounded-lg shadow p-8 text-center text-gray-600">
          {httpStatus(error) === 404
            ? 'No occurrence of this group matches the current filters (time range, version, build type, context).'
            : 'Could not load this exception group.'}
          <p className="text-xs text-gray-400 mt-2 font-mono break-all">{fingerprint}</p>
        </div>
      ) : (
        <div className="space-y-6">
          <Summary detail={data} />
          <TimeSeriesChart
            title="Occurrences over time"
            data={fillTrend(data.trend.points, data.trend.interval_seconds).map((p) => ({ timestamp: p.timestamp, value: p.count }))}
            startTime={queryParams.start_time}
            endTime={queryParams.end_time}
            color="#d97706"
            formatValue={(v) => formatNumber(v)}
          />
          <StackSection detail={data} />
          <DistributionSection fingerprint={fingerprint} />
          <OccurrencesSection fingerprint={fingerprint} />
        </div>
      )}
    </div>
  )
}

function BackLink() {
  return (
    <Link to="/exceptions" className="text-sm text-blue-600 hover:underline">
      ← Exceptions
    </Link>
  )
}

function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="bg-gray-50 rounded p-3" title={hint}>
      <p className="text-xs text-gray-500">{label}</p>
      <p className="text-sm font-medium text-gray-900 break-words">{value}</p>
    </div>
  )
}

function Summary({ detail: d }: { detail: Detail }) {
  const type = d.exception_type || d.sample.parsed.chain[0]?.type || ''
  const withSeq = d.sessions_with_seq ?? 0
  return (
    <div className="bg-white rounded-lg shadow p-6">
      {type && <p className="text-sm font-semibold text-gray-800 break-all">{type}</p>}
      <p className="font-mono text-sm text-gray-900 whitespace-pre-wrap break-all mt-1" title="The message's first line; the full text is under Raw text as sent">
        {(d.message || '').split('\n')[0] || '(no message)'}
      </p>
      <p className="text-xs text-gray-400 mt-1 font-mono break-all">
        {d.fingerprint}
        {d.fingerprint_version === 0 && ' (grouped by an SDK before APM-4)'}
      </p>
      <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3 mt-4">
        <Stat label="Occurrences" value={formatNumber(d.count)} />
        <Stat label="Sessions" value={formatNumber(d.session_count)} />
        <Stat
          label="First error of its session"
          value={withSeq ? `${percent(d.first_error_sessions ?? 0, withSeq)} (${d.first_error_sessions ?? 0} of ${withSeq})` : 'unknown'}
          hint="Share of sessions (whose SDK reports the error sequence) in which this was the first error: a high share suggests a root cause, a low one a follow-up error"
        />
        <Stat label="Unhandled" value={formatNumber(d.unhandled_count ?? 0)} hint="Occurrences no code caught" />
        <Stat label="First seen" value={`${formatDateTime(d.first_seen)}${d.first_version ? ` · ${d.first_version}` : ''}`} />
        <Stat label="Last seen" value={`${formatDateTime(d.last_seen)}${d.last_version ? ` · ${d.last_version}` : ''}`} />
      </div>
      <div className="mt-4">
        <IssueStatusControl key={d.fingerprint} kind="exception" fingerprint={d.fingerprint} info={d} />
      </div>
    </div>
  )
}

function FrameList({ frames }: { frames: ParsedFrame[] }) {
  if (frames.length === 0) {
    return <p className="text-xs text-gray-400 pl-4">No frames.</p>
  }
  return (
    <ol className="font-mono text-xs space-y-0.5 pl-4">
      {frames.map((f, i) => (
        <li key={i} className={f.in_app ? 'text-gray-900' : 'text-gray-400'} title={f.raw}>
          <span className={f.in_app ? 'font-semibold' : ''}>{f.method || f.raw}</span>
          {f.file && (
            <span className={f.in_app ? 'text-blue-700' : ''}>
              {' '}
              ({f.file}
              {f.line ? `:${f.line}` : ''})
            </span>
          )}
        </li>
      ))}
    </ol>
  )
}

function StackSection({ detail: d }: { detail: Detail }) {
  const s = d.sample
  const p = s.parsed
  return (
    <div className="bg-white rounded-lg shadow p-6">
      <h3 className="text-sm font-medium text-gray-700 mb-1">Stack</h3>
      <p className="text-xs text-gray-500 mb-3">
        Of the most recent occurrence ({formatDateTime(s.timestamp)}), parsed by the server. In-app frames are dark;
        file and line appear when the build has them.
      </p>
      {p.wrapped && (
        <p className="text-xs bg-amber-50 text-amber-800 rounded p-2 mb-3">
          This exception was caught and logged with an error message (e.g. by an event bus).
          {p.call_site && (
            <>
              {' '}Logged from <span className="font-mono">{p.call_site}</span>.
            </>
          )}
        </p>
      )}
      {p.chain.length > 0 ? (
        <div className="space-y-4">
          {p.chain.map((e, i) => (
            <div key={i}>
              <p className="text-xs font-semibold text-gray-800 break-all">
                {i > 0 && <span className="text-gray-500 font-normal">Inner exception: </span>}
                <span className="font-mono">{e.type}</span>
                {e.message && <span className="font-mono font-normal text-gray-700">: {e.message}</span>}
              </p>
              <FrameList frames={e.frames} />
            </div>
          ))}
        </div>
      ) : (
        <div>
          <p className="text-xs text-gray-600 mb-1">
            {p.kind === 'error' || p.kind === 'assert'
              ? `A logged ${p.kind} without an exception; the stack of the log call:`
              : 'No exception chain could be parsed.'}
          </p>
          <FrameList frames={p.log_frames} />
        </div>
      )}
      {p.wrapped && p.log_frames.length > 0 && (
        <details className="mt-3">
          <summary className="text-xs text-gray-500 cursor-pointer">Stack of the log call ({p.log_frames.length} frames)</summary>
          <FrameList frames={p.log_frames} />
        </details>
      )}
      {noFileLines(p) && (
        <p className="text-xs text-gray-500 mt-3" data-testid="no-file-line">
          No file or line in this stack: an IL2CPP release build reports them only with IL2CPP Stacktrace Information
          &quot;Method Name, File Name, and Line Number&quot; (Player Settings, Unity 2023.1 and newer; RELEASE.md, APM-7).
          Native crash stacks get them from the uploaded symbols.
        </p>
      )}
      {s.inner_exceptions.length > 0 && (
        <p className="text-xs text-gray-500 mt-3">
          Inner exceptions: <span className="font-mono">{s.inner_exceptions.join(' → ')}</span>
        </p>
      )}
      <details className="mt-3">
        <summary className="text-xs text-gray-500 cursor-pointer">Raw text as sent</summary>
        <div className="mt-2 space-y-2">
          <RawBlock label="Message" text={s.message} />
          <RawBlock label="Stack" text={s.stack} />
          <RawBlock label="Embedded exception" text={s.embedded_exception} />
        </div>
      </details>
    </div>
  )
}

// True when the stack has in-app frames and none of them has a file: a
// release IL2CPP build without line information.
function noFileLines(p: Detail['sample']['parsed']): boolean {
  const frames = [...p.chain.flatMap((e) => e.frames), ...p.log_frames].filter((f) => f.in_app)
  return frames.length > 0 && frames.every((f) => !f.file)
}

function RawBlock({ label, text }: { label: string; text: string }) {
  if (!text) return null
  return (
    <div>
      <p className="text-xs text-gray-500">{label}</p>
      <pre className="text-xs bg-gray-50 p-2 rounded overflow-auto max-h-64 font-mono whitespace-pre-wrap break-all">{text}</pre>
    </div>
  )
}

function DistributionSection({ fingerprint }: { fingerprint: string }) {
  const { queryParams, addContextFilter } = useFilters()
  const [group, setGroup] = useState<ExceptionGroupBy>({ kind: 'by', key: 'app_version' })
  const [keyInput, setKeyInput] = useState('')
  const { data: keyData } = useContextKeys({ ...queryParams, source: 'exceptions' })
  const keys = keyData?.keys ?? []
  const { data, isLoading, isError } = useExceptionDistribution(fingerprint, group, queryParams)
  const rows = data?.rows ?? []
  const max = rows.reduce((m, r) => Math.max(m, r.count), 0)
  const selectValue = group.kind === 'by' ? `by:${group.key}` : 'key:'

  return (
    <div className="bg-white rounded-lg shadow p-6">
      <div className="flex flex-wrap items-center gap-3 mb-3">
        <h3 className="text-sm font-medium text-gray-700">Distribution</h3>
        <select
          aria-label="Distribution by"
          value={selectValue}
          onChange={(e) => {
            const v = e.target.value
            if (v.startsWith('by:')) {
              setGroup({ kind: 'by', key: v.slice(3) as ExceptionColumn })
            } else {
              const k = keyInput || keys[0]?.key || ''
              setKeyInput(k)
              setGroup({ kind: 'key', key: k })
            }
          }}
          className="px-2 py-1 text-sm border border-gray-300 rounded"
        >
          {columns.map((c) => (
            <option key={c.value} value={`by:${c.value}`}>
              {c.label}
            </option>
          ))}
          <option value="key:">Context key…</option>
        </select>
        {group.kind === 'key' && (
          <>
            <input
              aria-label="Context key"
              list="ozx-exception-context-keys"
              value={keyInput}
              placeholder="e.g. roomId"
              onChange={(e) => {
                const k = e.target.value.trim()
                setKeyInput(k)
                if (isValidContextKey(k)) setGroup({ kind: 'key', key: k })
              }}
              className="w-40 px-2 py-1 text-sm border border-gray-300 rounded font-mono"
            />
            <datalist id="ozx-exception-context-keys">
              {keys.map((k) => (
                <option key={k.key} value={k.key} />
              ))}
            </datalist>
          </>
        )}
      </div>
      {isLoading ? (
        <p className="text-sm text-gray-500">Loading...</p>
      ) : isError ? (
        <p className="text-sm text-red-600">Could not load the distribution.</p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-gray-500">{group.kind === 'key' && !group.key ? 'Pick a context key.' : 'No occurrences.'}</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-xs text-gray-500">
              <th className="py-1">{group.kind === 'key' ? group.key : columns.find((c) => c.value === group.key)?.label}</th>
              <th className="py-1 w-1/2">Occurrences</th>
              <th className="py-1 text-right">Sessions</th>
              {group.kind === 'key' && <th />}
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.value} className="border-t border-gray-100">
                <td className="py-1 font-mono text-xs break-all">{r.value === '' ? <span className="text-gray-400">(not set)</span> : r.value}</td>
                <td className="py-1">
                  <div className="flex items-center gap-2">
                    <div className="h-2 bg-amber-400 rounded" style={{ width: `${max ? Math.max(2, (r.count / max) * 100) : 0}%` }} />
                    <span className="text-xs text-gray-700">
                      {formatNumber(r.count)} ({percent(r.count, data?.total ?? 0)})
                    </span>
                  </div>
                </td>
                <td className="py-1 text-right text-xs">{formatNumber(r.sessions)}</td>
                {group.kind === 'key' && (
                  <td className="py-1 text-right">
                    <button
                      className="text-xs text-blue-600 hover:underline"
                      onClick={() => addContextFilter({ key: group.key, value: r.value })}
                    >
                      Filter
                    </button>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {(data?.merged ?? 0) > 0 && (
        <p className="text-xs text-gray-500 mt-2">
          {formatNumber(data!.merged)} of {formatNumber(data!.total)} occurrences are not in the table: the SDK counted
          them together across different values of this key.
        </p>
      )}
      {data?.truncated && <p className="text-xs text-gray-500 mt-1">Only the 50 most frequent values are shown.</p>}
    </div>
  )
}

// The server pages through the 10000 most recent rows of a group only.
const MAX_PAGED_OCCURRENCES = 10000

function OccurrencesSection({ fingerprint }: { fingerprint: string }) {
  const { queryParams } = useFilters()
  const [page, setPage] = useState(1)
  const pageSize = 10
  // Another filter starts at the first page again.
  const filterKey = JSON.stringify(queryParams)
  useEffect(() => setPage(1), [filterKey, fingerprint])
  const { data, isLoading, isError } = useExceptionOccurrences(fingerprint, { ...queryParams, page, page_size: pageSize })
  const occurrences = data?.occurrences ?? []
  return (
    <div className="bg-white rounded-lg shadow p-6">
      <h3 className="text-sm font-medium text-gray-700 mb-1">Occurrences</h3>
      <p className="text-xs text-gray-500 mb-3">
        Most recent first. A row is a first occurrence (with breadcrumbs) or the count of its repeats in one game context
        within a minute.
      </p>
      {isLoading ? (
        <p className="text-sm text-gray-500">Loading...</p>
      ) : isError ? (
        <p className="text-sm text-red-600">Could not load the occurrences.</p>
      ) : occurrences.length === 0 ? (
        <p className="text-sm text-gray-500">No occurrences.</p>
      ) : (
        <div className="space-y-4">
          {occurrences.map((o) => (
            <OccurrenceCard key={o.event_id || o.timestamp} occurrence={o} />
          ))}
        </div>
      )}
      {data && (
        <Pagination
          page={data.page}
          pageSize={data.page_size}
          totalCount={Math.min(data.total_count, MAX_PAGED_OCCURRENCES)}
          onPageChange={setPage}
        />
      )}
      {data && data.total_count > MAX_PAGED_OCCURRENCES && (
        <p className="text-xs text-gray-500 mt-2">
          {formatNumber(data.total_count)} rows match; the {formatNumber(MAX_PAGED_OCCURRENCES)} most recent can be paged
          through. Narrow the time range for older ones.
        </p>
      )}
    </div>
  )
}

function Badge({ children, className }: { children: ReactNode; className: string }) {
  return <span className={`inline-block text-xs px-1.5 py-0.5 rounded ${className}`}>{children}</span>
}

export function OccurrenceCard({ occurrence: o }: { occurrence: ExceptionOccurrence }) {
  const ctx = Object.entries(o.context).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
  return (
    <div className="border border-gray-200 rounded p-3" data-testid="occurrence">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <span className="font-medium text-gray-900">{formatDateTime(o.timestamp)}</span>
        <Badge className="bg-amber-100 text-amber-800">×{formatNumber(o.count)}</Badge>
        {o.error_seq === 1 && <Badge className="bg-red-100 text-red-800">first error of the session</Badge>}
        {o.error_seq > 1 && <Badge className="bg-gray-100 text-gray-700">error #{o.error_seq} of the session</Badge>}
        {o.session_ms > 0 && <span className="text-gray-500">{formatSessionMs(o.session_ms)} into the session</span>}
        {o.unhandled && <Badge className="bg-red-600 text-white">unhandled ({o.unhandled})</Badge>}
        {o.ctx_merged && (
          <Badge className="bg-gray-100 text-gray-600">
            <span title="This count merges occurrences in different game contexts; the context shows only what they shared">
              merged contexts
            </span>
          </Badge>
        )}
      </div>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-x-6 gap-y-1 mt-2 text-xs text-gray-700">
        <p>
          <span className="text-gray-500">Version:</span> {o.app_version || '-'}
          {o.build_type ? ` (${o.build_type})` : ''}
        </p>
        <p>
          <span className="text-gray-500">Device:</span> {o.device_model || '-'} · {o.platform || '-'} {o.os_version}
        </p>
        <p>
          <span className="text-gray-500">Scene:</span> {o.scene || '-'}
        </p>
        <p className="break-all">
          <span className="text-gray-500">Session:</span>{' '}
          {o.session_id ? (
            <>
              <span className="font-mono">{o.session_id}</span>{' '}
              <Link to={sessionLink(o)} className="text-blue-600 hover:underline">
                Session time line at this moment
              </Link>
            </>
          ) : (
            '-'
          )}
        </p>
        {o.device_id && (
          <p className="break-all md:col-span-2">
            <span className="text-gray-500">Device id:</span> <span className="font-mono">{o.device_id}</span>{' '}
            <Link to={deviceSessionsLink(o.device_id, o.timestamp)} className="text-blue-600 hover:underline">
              Its sessions
            </Link>
          </p>
        )}
      </div>
      {ctx.length > 0 ? (
        <details className="mt-2" open>
          <summary className="text-xs text-gray-500 cursor-pointer">Game context ({ctx.length})</summary>
          <table className="mt-1 text-xs font-mono">
            <tbody>
              {ctx.map(([k, v]) => (
                <tr key={k}>
                  <td className="pr-3 text-gray-500 align-top">{k}</td>
                  <td className="text-gray-800 break-all">{v}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      ) : (
        <p className="text-xs text-gray-400 mt-2">No game context.</p>
      )}
      <details className="mt-2" open={o.breadcrumbs.length > 0}>
        <summary className="text-xs text-gray-500 cursor-pointer">Breadcrumbs ({o.breadcrumbs.length})</summary>
        <div className="mt-2">
          <BreadcrumbList lines={o.breadcrumbs} endLabel={o.breadcrumbs.length > 0 ? 'This exception' : undefined} />
        </div>
      </details>
    </div>
  )
}
