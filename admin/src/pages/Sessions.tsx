import { useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { TimeSeriesChart } from '../components/TimeSeriesChart'
import { SessionRecordings } from '../components/SessionRecordings'
import { useDeviceSessions, useSessionTimeline } from '../hooks/useApi'
import { formatDateTime, formatMs, formatNumber } from '../utils/format'
import { deviceSessionsLink, exceptionDetailLink, formatSessionMs, kindStyles, sessionExportLink, utcClock } from '../utils/timeline'
import type { SessionTimeline, TimelineItem, TimelineType } from '../types/api'

const typeLabels: Record<TimelineType, { label: string; className: string }> = {
  custom_event: { label: 'Event', className: 'bg-blue-100 text-blue-800' },
  custom_metric: { label: 'Metric', className: 'bg-teal-100 text-teal-800' },
  exception: { label: 'Exception', className: 'bg-orange-100 text-orange-800' },
  crash: { label: 'Crash', className: 'bg-red-600 text-white' },
  jank: { label: 'Jank', className: 'bg-pink-100 text-pink-800' },
  scene_load: { label: 'Scene load', className: 'bg-purple-100 text-purple-800' },
  startup: { label: 'Startup', className: 'bg-green-100 text-green-800' },
  run: { label: 'Run', className: 'bg-indigo-100 text-indigo-800' },
  context: { label: 'Context', className: 'bg-slate-100 text-slate-700' },
  breadcrumb: { label: 'Log / breadcrumb', className: 'bg-gray-100 text-gray-700' },
}

const allTypes = Object.keys(typeLabels) as TimelineType[]

const ranges: { value: string; label: string; ms: number }[] = [
  { value: '24h', label: 'Last 24 hours', ms: 24 * 3600 * 1000 },
  { value: '7d', label: 'Last 7 days', ms: 7 * 24 * 3600 * 1000 },
  { value: '30d', label: 'Last 30 days', ms: 30 * 24 * 3600 * 1000 },
]

function httpStatus(err: unknown): number {
  return (err as { response?: { status?: number } } | null)?.response?.status ?? 0
}

function errorText(err: unknown): string {
  const data = (err as { response?: { data?: unknown } } | null)?.response?.data
  return typeof data === 'string' ? data.slice(0, 200) : ''
}

// The session time line (APM-15), and a device's sessions. Opened from an
// exception occurrence (session_id + at: the occurrence's moment), by a
// session id or a device id typed in the search box.
export function Sessions() {
  const [search, setSearch] = useSearchParams()
  const sessionId = search.get('session_id') || ''
  const deviceId = search.get('device_id') || ''
  const [sessionInput, setSessionInput] = useState(sessionId)
  const [deviceInput, setDeviceInput] = useState(deviceId)
  const [range, setRange] = useState('7d')

  useEffect(() => setSessionInput(sessionId), [sessionId])
  useEffect(() => setDeviceInput(deviceId), [deviceId])

  const open = (e: FormEvent, params: Record<string, string>) => {
    e.preventDefault()
    const r = ranges.find((x) => x.value === range) ?? ranges[1]
    const end = new Date()
    setSearch({ ...params, start_time: new Date(end.getTime() - r.ms).toISOString(), end_time: end.toISOString() })
  }

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Sessions</h1>
      <div className="bg-white rounded-lg shadow p-4 mb-6 flex flex-wrap items-end gap-4">
        <form className="flex items-end gap-2" onSubmit={(e) => sessionInput.trim() && open(e, { session_id: sessionInput.trim() })}>
          <label className="text-sm text-gray-500">
            Session id
            <input
              aria-label="Session id"
              value={sessionInput}
              onChange={(e) => setSessionInput(e.target.value)}
              className="block w-72 px-2 py-1 mt-1 text-sm border border-gray-300 rounded font-mono"
              placeholder="1729245600000_a1b2c3d4"
            />
          </label>
          <button type="submit" className="px-3 py-1 text-sm rounded bg-blue-500 text-white disabled:opacity-50" disabled={!sessionInput.trim()}>
            Open time line
          </button>
        </form>
        <form className="flex items-end gap-2" onSubmit={(e) => deviceInput.trim() && open(e, { device_id: deviceInput.trim() })}>
          <label className="text-sm text-gray-500">
            Device id
            <input
              aria-label="Device id"
              value={deviceInput}
              onChange={(e) => setDeviceInput(e.target.value)}
              className="block w-72 px-2 py-1 mt-1 text-sm border border-gray-300 rounded font-mono"
            />
          </label>
          <button type="submit" className="px-3 py-1 text-sm rounded bg-gray-100 text-gray-700 disabled:opacity-50" disabled={!deviceInput.trim()}>
            List sessions
          </button>
        </form>
        <label className="text-sm text-gray-500">
          Search in
          <select
            aria-label="Search range"
            value={range}
            onChange={(e) => setRange(e.target.value)}
            className="block px-2 py-1 mt-1 text-sm border border-gray-300 rounded"
          >
            {ranges.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </select>
        </label>
      </div>

      {sessionId ? (
        <SessionView
          sessionId={sessionId}
          at={search.get('at') || ''}
          startTime={search.get('start_time') || ''}
          endTime={search.get('end_time') || ''}
        />
      ) : deviceId ? (
        <DeviceSessions deviceId={deviceId} startTime={search.get('start_time') || ''} endTime={search.get('end_time') || ''} />
      ) : (
        <p className="text-sm text-gray-500">
          Open a session from an exception occurrence, or enter a session id or a device id.
        </p>
      )}
    </div>
  )
}

function DeviceSessions({ deviceId, startTime, endTime }: { deviceId: string; startTime: string; endTime: string }) {
  const params = useMemo(
    () => ({ ...(startTime ? { start_time: startTime } : {}), ...(endTime ? { end_time: endTime } : {}) }),
    [startTime, endTime]
  )
  const { data, isLoading, isError, error } = useDeviceSessions(deviceId, params)
  if (isLoading) return <div className="bg-white rounded-lg shadow p-8 text-center text-gray-500">Loading...</div>
  if (isError || !data) {
    return (
      <div className="bg-white rounded-lg shadow p-8 text-center text-red-600">
        Could not load the sessions{httpStatus(error) === 400 ? `: ${errorText(error)}` : '.'}
      </div>
    )
  }
  return (
    <div className="bg-white rounded-lg shadow p-6">
      <h3 className="text-sm font-medium text-gray-700 mb-1">
        Sessions of device <span className="font-mono break-all">{deviceId}</span>
      </h3>
      <p className="text-xs text-gray-500 mb-3">
        {formatDateTime(data.start_time)} - {formatDateTime(data.end_time)}, most recent first.
      </p>
      {data.sessions.length === 0 ? (
        <p className="text-sm text-gray-500">No data from this device in this range.</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-xs text-gray-500">
              <th className="py-1">Session</th>
              <th className="py-1">From</th>
              <th className="py-1">To</th>
              <th className="py-1">Version</th>
              <th className="py-1 text-right">Rows</th>
              <th className="py-1 text-right">Exceptions</th>
              <th className="py-1 text-right">Crashes</th>
            </tr>
          </thead>
          <tbody>
            {data.sessions.map((s) => (
              <tr key={s.session_id} className="border-t border-gray-100">
                <td className="py-1 font-mono text-xs break-all">
                  <Link
                    className="text-blue-600 hover:underline"
                    to={
                      '/sessions?' +
                      new URLSearchParams({
                        session_id: s.session_id,
                        start_time: s.first_seen,
                        end_time: s.last_seen,
                      }).toString()
                    }
                  >
                    {s.session_id}
                  </Link>
                </td>
                <td className="py-1 text-xs">{formatDateTime(s.first_seen)}</td>
                <td className="py-1 text-xs">{formatDateTime(s.last_seen)}</td>
                <td className="py-1 text-xs">
                  {s.app_version}
                  {s.build_type ? ` (${s.build_type})` : ''}
                </td>
                <td className="py-1 text-xs text-right">{formatNumber(s.rows)}</td>
                <td className="py-1 text-xs text-right">{formatNumber(s.exceptions)}</td>
                <td className="py-1 text-xs text-right">{formatNumber(s.crashes)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {data.truncated && <p className="text-xs text-gray-500 mt-2">Only the 100 most recent sessions are listed.</p>}
    </div>
  )
}

interface SessionViewProps {
  sessionId: string
  at: string
  startTime: string
  endTime: string
}

function SessionView({ sessionId, at, startTime, endTime }: SessionViewProps) {
  const [perf, setPerf] = useState(false)
  const [hidden, setHidden] = useState<Set<TimelineType>>(new Set())
  const params = useMemo(
    () => ({
      session_id: sessionId,
      ...(at ? { at } : {}),
      ...(startTime ? { start_time: startTime } : {}),
      ...(endTime ? { end_time: endTime } : {}),
      ...(perf ? { perf: '1' } : {}),
    }),
    [sessionId, at, startTime, endTime, perf]
  )
  const { data, isLoading, isError, error } = useSessionTimeline(params)

  if (isLoading) return <div className="bg-white rounded-lg shadow p-8 text-center text-gray-500">Loading...</div>
  if (isError || !data) {
    return (
      <div className="bg-white rounded-lg shadow p-8 text-center text-red-600">
        Could not load the session{httpStatus(error) === 400 ? `: ${errorText(error)}` : '.'}
      </div>
    )
  }
  if (!data.found) {
    return (
      <div className="bg-white rounded-lg shadow p-8 text-center text-gray-600">
        No data of session <span className="font-mono break-all">{sessionId}</span> between {formatDateTime(data.start_time)} and{' '}
        {formatDateTime(data.end_time)}. Data is kept 30 days; try a longer range.
      </div>
    )
  }

  const toggle = (t: TimelineType) =>
    setHidden((prev) => {
      const next = new Set(prev)
      if (next.has(t)) next.delete(t)
      else next.add(t)
      return next
    })

  return (
    <div className="space-y-6">
      <SessionHeader timeline={data} />
      <div className="bg-white rounded-lg shadow p-4 flex flex-wrap items-center gap-3 text-sm">
        <span className="text-gray-500">Show:</span>
        {allTypes.map((t) => (
          <label key={t} className="flex items-center gap-1">
            <input type="checkbox" checked={!hidden.has(t)} onChange={() => toggle(t)} aria-label={`Show ${typeLabels[t].label}`} />
            <span className={`px-1.5 rounded text-xs ${typeLabels[t].className}`}>{typeLabels[t].label}</span>
            <span className="text-xs text-gray-400">{data.counts[t] ?? 0}</span>
          </label>
        ))}
        <label className="flex items-center gap-1 ml-auto">
          <input type="checkbox" checked={perf} onChange={(e) => setPerf(e.target.checked)} />
          <span className="text-gray-700">Performance track</span>
        </label>
      </div>
      {perf && (
        <TimeSeriesChart
          title={`FPS (average per ${data.perf_interval_seconds || '-'} s)`}
          data={data.perf.map((p) => ({ timestamp: p.time, value: p.fps_avg }))}
          startTime={data.session.first_seen}
          endTime={data.session.last_seen}
          color="#16a34a"
        />
      )}
      {data.truncated.length > 0 && (
        <p className="text-xs bg-amber-50 text-amber-800 rounded p-2">
          More data than shown for: {data.truncated.map((t) => typeLabels[t as TimelineType]?.label ?? t).join(', ')}.
          {at ? ' The items nearest to the selected moment are shown (for context changes, the earliest).' : ' The earliest items are shown.'} Narrow the time
          range to see the rest.
        </p>
      )}
      <TimelineList timeline={data} at={at} hidden={hidden} />
    </div>
  )
}

function SessionHeader({ timeline: t }: { timeline: SessionTimeline }) {
  const s = t.session
  return (
    <div className="bg-white rounded-lg shadow p-6">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <h3 className="text-sm font-medium text-gray-700">
          Session <span className="font-mono break-all">{s.session_id}</span>{' '}
          <SessionRecordings sessionId={s.session_id} />
        </h3>
        <a
          href={sessionExportLink(s.session_id, t.start_time, t.end_time)}
          download
          className="px-3 py-1 text-sm rounded bg-gray-100 text-gray-700 hover:bg-gray-200"
          title="A zip of the session in OZX's AiTelemetry format (session.json, one JSONL file per channel, apm.jsonl), for scripts/ai_trace.py and verify-ticket"
        >
          Download (AiTelemetry JSONL)
        </a>
      </div>
      <div className="grid grid-cols-1 md:grid-cols-3 gap-x-6 gap-y-1 mt-2 text-xs text-gray-700">
        <p>
          <span className="text-gray-500">Version:</span> {s.app_version || '-'}
          {s.build_type ? ` (${s.build_type})` : ''}
        </p>
        <p>
          <span className="text-gray-500">Device:</span> {s.device_model || '-'} · {s.platform || '-'} {s.os_version}
        </p>
        <p>
          <span className="text-gray-500">Data:</span> {formatDateTime(s.first_seen)} - {formatDateTime(s.last_seen)} ({formatNumber(s.rows)} rows)
        </p>
        {s.started_at && (
          <p>
            <span className="text-gray-500">Session started:</span> {formatDateTime(s.started_at)}
          </p>
        )}
        {s.device_id && (
          <p className="md:col-span-2 break-all">
            <span className="text-gray-500">Device id:</span> <span className="font-mono">{s.device_id}</span>{' '}
            <Link to={deviceSessionsLink(s.device_id, s.last_seen)} className="text-blue-600 hover:underline">
              Other sessions of this device
            </Link>
          </p>
        )}
      </div>
      <p className="text-xs text-gray-400 mt-2">
        Times are UTC. Context changes are seen at the next row that carries the context (a perf sample every second when
        sampling is on). Breadcrumb lines come from the exceptions, janks and crashes that carried them; a line also stored
        as its own row is shown once ({t.duplicate_breadcrumbs} duplicates hidden).
      </p>
    </div>
  )
}

// nearestIndex: the item to anchor on: the exception at exactly at, else the
// item nearest to it.
function nearestIndex(items: TimelineItem[], at: string): number {
  const t = Date.parse(at)
  if (Number.isNaN(t) || items.length === 0) return -1
  let best = -1
  let bestScore = Infinity
  items.forEach((it, i) => {
    const d = Math.abs(Date.parse(it.time) - t)
    // An exception at that moment wins over anything else at the same distance.
    const score = d * 2 + (it.type === 'exception' ? 0 : 1)
    if (score < bestScore) {
      bestScore = score
      best = i
    }
  })
  return best
}

function TimelineList({ timeline, at, hidden }: { timeline: SessionTimeline; at: string; hidden: Set<TimelineType> }) {
  const items = timeline.items.filter((it) => !hidden.has(it.type))
  const anchor = nearestIndex(items, at)
  const anchorRef = useRef<HTMLLIElement | null>(null)
  const start = Date.parse(timeline.session.started_at || timeline.session.first_seen)

  useEffect(() => {
    // jsdom has no scrollIntoView.
    anchorRef.current?.scrollIntoView?.({ block: 'center' })
  }, [anchor, timeline])

  return (
    <div className="bg-white rounded-lg shadow p-6">
      <ol className="border-l-2 border-gray-200 ml-2 space-y-2" aria-label="Session time line">
        {timeline.session.started_at && (
          <Marker time={timeline.session.started_at} text="Session started" />
        )}
        {items.map((it, i) => {
          const rel = Date.parse(it.time) - start
          return (
            <li
              key={`${it.type}-${it.event_id || ''}-${it.time}-${i}`}
              ref={i === anchor ? anchorRef : undefined}
              data-type={it.type}
              data-anchor={i === anchor ? 'true' : undefined}
              className={`relative pl-4 py-1 text-sm ${i === anchor ? 'bg-yellow-50 ring-2 ring-yellow-300 rounded' : ''}`}
            >
              <span className="absolute -left-[5px] top-2.5 w-2 h-2 rounded-full bg-gray-300" />
              <div className="flex flex-wrap items-baseline gap-2">
                <span className="font-mono text-xs text-gray-500" title={it.time}>
                  {utcClock(it.time)}
                  {it.time_unknown ? ' ?' : ''}
                </span>
                {rel >= 0 && <span className="text-xs text-gray-400 w-16">+{formatSessionMs(rel) || '0ms'}</span>}
                <span className={`px-1.5 rounded text-xs ${typeLabels[it.type]?.className ?? 'bg-gray-100'}`}>
                  {typeLabels[it.type]?.label ?? it.type}
                </span>
                <div className="flex-1 min-w-0">
                  <ItemBody item={it} />
                </div>
              </div>
            </li>
          )
        })}
        <Marker
          time={timeline.session.last_seen}
          text="Last data from this session (the app was closed, killed, went to the background for good, or stopped sending)"
        />
      </ol>
      {items.length === 0 && <p className="text-sm text-gray-500 mt-2">No items of the selected types.</p>}
    </div>
  )
}

function Marker({ time, text }: { time: string; text: string }) {
  return (
    <li className="relative pl-4 py-1 text-xs text-gray-500 italic">
      <span className="absolute -left-[5px] top-2 w-2 h-2 rounded-full bg-gray-500" />
      <span className="font-mono mr-2">{utcClock(time)}</span>
      {text}
    </li>
  )
}

function Props({ values }: { values?: Record<string, string> }) {
  const entries = Object.entries(values ?? {}).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
  if (entries.length === 0) return null
  return (
    <span className="font-mono text-xs text-gray-600 break-all">
      {entries.map(([k, v]) => `${k}=${v}`).join(' ')}
    </span>
  )
}

function firstLine(s?: string): string {
  return (s ?? '').split('\n')[0]
}

function Details({ summary, children }: { summary: string; children: ReactNode }) {
  return (
    <details className="mt-1">
      <summary className="text-xs text-gray-500 cursor-pointer">{summary}</summary>
      <div className="mt-1">{children}</div>
    </details>
  )
}

// Game text cut for one line (run ids, results).
function clip(s: string, max = 120): string {
  return s.length > max ? s.slice(0, max) + '…' : s
}

function ItemBody({ item: it }: { item: TimelineItem }) {
  switch (it.type) {
    case 'custom_event':
      return (
        <div>
          <span className="font-mono font-medium">{it.name}</span>{' '}
          {it.aggregated ? (
            <span className="text-xs text-gray-600" title="Calls beyond the SDK's per-minute budget, counted from this time to the end of the SDK's minute; sent without properties">
              ×{formatNumber(it.count ?? 0)} calls (counted)
            </span>
          ) : (
            <Props values={it.props} />
          )}
          {it.ctx_merged && <span className="text-xs text-gray-400"> · contexts merged</span>}
        </div>
      )
    case 'custom_metric':
      return (
        <div>
          <span className="font-mono font-medium">{it.name}</span>{' '}
          <span className="text-xs text-gray-600" title="One row aggregates the samples of a metric in one game context over the SDK's window (from this time)">
            {formatNumber(it.count ?? 0)} samples, min {it.min ?? 0} / avg {it.count ? ((it.sum ?? 0) / it.count).toFixed(2) : 0} / max{' '}
            {it.max ?? 0}
            {it.unit ? ` ${it.unit}` : ''} (window aggregate)
          </span>
        </div>
      )
    case 'exception':
      return (
        <div>
          <span className="font-medium break-all">{it.exception_type || firstLine(it.message) || '(exception)'}</span>
          {(it.count ?? 1) > 1 && <span className="text-xs text-gray-600"> ×{formatNumber(it.count ?? 0)}</span>}
          {it.error_seq === 1 && <span className="ml-1 text-xs px-1 rounded bg-red-100 text-red-800">first error</span>}
          {(it.error_seq ?? 0) > 1 && <span className="ml-1 text-xs text-gray-500">error #{it.error_seq}</span>}
          {it.unhandled && <span className="ml-1 text-xs px-1 rounded bg-red-600 text-white">unhandled</span>}
          {it.fingerprint && (
            <Link to={exceptionDetailLink(it.fingerprint)} className="ml-2 text-xs text-blue-600 hover:underline">
              details
            </Link>
          )}
          <p className="font-mono text-xs text-gray-700 break-all">{firstLine(it.message)}</p>
          {it.throw_site && <p className="font-mono text-xs text-gray-500 break-all">at {it.throw_site}</p>}
        </div>
      )
    case 'crash':
      return (
        <div>
          <span className="font-medium">{it.crash_type || 'crash'}</span>
          <p className="font-mono text-xs text-gray-700 break-all">{firstLine(it.message)}</p>
        </div>
      )
    case 'jank':
      return (
        <div>
          <span>
            {formatMs(it.duration_ms ?? 0)} (longest frame {formatMs(it.max_frame_ms ?? 0)}), {it.recent_gc_count ?? 0} GCs
          </span>
          {it.scene && <span className="text-xs text-gray-500"> in {it.scene}</span>}
          {it.recent_events && it.recent_events.length > 0 && (
            <Details summary={`Recent events (${it.recent_events.length})`}>
              <ul className="font-mono text-xs text-gray-600">
                {it.recent_events.map((e, i) => (
                  <li key={i} className="break-all">
                    {e}
                  </li>
                ))}
              </ul>
            </Details>
          )}
        </div>
      )
    case 'scene_load':
      return (
        <span>
          <span className="font-mono">{it.scene_name}</span> loaded in {formatMs(it.load_ms ?? 0)}
          {it.activate_ms ? ` (+${formatMs(it.activate_ms)} activation)` : ''}
        </span>
      )
    case 'startup':
      return (
        <span className="text-xs">
          app → Unity {formatMs(it.phase1_ms ?? 0)}, Unity → first frame {formatMs(it.phase2_ms ?? 0)}, → interactive{' '}
          {formatMs(it.tti_ms ?? 0)}
        </span>
      )
    case 'run':
      return (
        <div className="text-xs">
          <span className="font-mono font-medium break-all">{clip(it.run_id ?? '')}</span>{' '}
          <span className={it.end_reason === 'crash' ? 'text-red-700' : 'text-gray-700'}>
            ended: {it.end_reason || '?'}
            {it.crash_type ? ` (${it.crash_type})` : ''}
          </span>
          {it.result && <span className="text-gray-600 break-all"> · result {clip(it.result)}</span>}
          {it.duration_ms ? <span className="text-gray-600"> · {formatMs(it.duration_ms)}</span> : null}
          {!!(it.peak_rss_mb || it.peak_alloc_mb || it.peak_mem_mb) && (
            <span className="text-gray-600">
              {' '}
              · peak{it.peak_rss_mb ? ` RSS ${Math.round(it.peak_rss_mb)} MB` : ''}
              {it.peak_alloc_mb ? ` native ${Math.round(it.peak_alloc_mb)} MB` : ''}
              {it.peak_mem_mb ? ` managed ${Math.round(it.peak_mem_mb)} MB` : ''}
            </span>
          )}
        </div>
      )
    case 'context':
      return (
        <div className="text-xs">
          {(it.changes ?? []).map((c) => (
            <span key={c.key} className="inline-block mr-3 font-mono break-all">
              {c.key}: {c.from === '' ? <span className="text-gray-400">(unset)</span> : c.from} →{' '}
              {c.to === '' ? <span className="text-gray-400">(unset)</span> : <span className="font-semibold">{c.to}</span>}
            </span>
          ))}
          {it.source && <span className="text-gray-400">(seen on {typeLabels[it.source as TimelineType]?.label ?? it.source})</span>}
        </div>
      )
    case 'breadcrumb': {
      const style = kindStyles[it.kind ?? 'text'] ?? kindStyles.text
      return (
        <div className="text-xs">
          <span className={`inline-block px-1.5 rounded mr-2 ${style.className}`}>{style.label}</span>
          <span className="font-mono text-gray-800 break-all">{it.text}</span>
          {(it.repeats ?? 0) > 0 && (
            <span className="ml-2 text-gray-500">
              ×{it.repeats}
              {it.repeat_until ? ` until ${utcClock(it.repeat_until)}` : ''}
            </span>
          )}
          {it.source && <span className="ml-2 text-gray-400">(from a {typeLabels[it.source as TimelineType]?.label.toLowerCase() ?? it.source})</span>}
        </div>
      )
    }
  }
  return null
}
