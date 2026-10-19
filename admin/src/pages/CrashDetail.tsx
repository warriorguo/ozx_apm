import type { ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { GlobalFilterBar } from '../components/FilterBar'
import { BreadcrumbList } from '../components/BreadcrumbList'
import { IssueStatusControl } from '../components/IssueStatus'
import { useFilters } from '../hooks/useFilters'
import { useCrashDetail } from '../hooks/useApi'
import { formatDateTime, formatNumber } from '../utils/format'
import { deviceSessionsLink, formatSessionMs, sessionLink } from '../utils/timeline'
import type { CrashDetail as Detail, CrashFrame, CrashOccurrence, CrashSymbolStatus, CrashThread } from '../types/api'

function httpStatus(err: unknown): number {
  const r = (err as { response?: { status?: number } } | null)?.response
  return r?.status ?? 0
}

// What a crash type means, for the reader.
export const crashTypeLabels: Record<string, string> = {
  native: 'Native crash',
  anr: 'ANR (not responding)',
  java: 'Java crash',
  oom: 'Killed for low memory',
  killed: 'Killed by the system',
  unknown: 'Ended without a clean exit',
}

export function crashDetailLink(fingerprint: string): string {
  return '/crashes/detail?' + new URLSearchParams({ fingerprint }).toString()
}

// One frame on one line: the function (C# method when symbolicated) and its
// file:line, else the library and address.
export function frameLabel(f: CrashFrame): string {
  if (!f.native) return f.function || f.raw
  if (!f.function) return `${f.module || '?'} + ${f.pc || '?'}`
  return f.function
}

// The crash detail page (APM-7): one group (fingerprint) with its latest
// crash: what Android said about the exit, the stack symbolicated with the
// uploaded symbols (C# method, file and line), the game context and the
// breadcrumbs before the crash, and the recent crashes of the group.
export function CrashDetail() {
  const [search] = useSearchParams()
  const fingerprint = search.get('fingerprint') || ''
  const { queryParams } = useFilters()
  const { data, isLoading, isError, error } = useCrashDetail(fingerprint, queryParams)

  if (!fingerprint) {
    return (
      <div>
        <BackLink />
        <p className="text-gray-500">No crash group selected.</p>
      </div>
    )
  }
  return (
    <div>
      <BackLink />
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Crash</h1>
      <GlobalFilterBar />
      {isLoading ? (
        <div className="bg-white rounded-lg shadow p-8 text-center text-gray-500">Loading...</div>
      ) : isError || !data ? (
        <div className="bg-white rounded-lg shadow p-8 text-center text-gray-600">
          {httpStatus(error) === 404
            ? 'No crash of this group matches the current filters (time range, version, build type, context).'
            : 'Could not load this crash group.'}
          <p className="text-xs text-gray-400 mt-2 font-mono break-all">{fingerprint}</p>
        </div>
      ) : (
        <div className="space-y-6">
          <Summary detail={data} />
          {data.sample && <ExitSection sample={data.sample} />}
          {data.sample && <MemorySection sample={data.sample} />}
          <StackSection detail={data} />
          {data.sample && <ContextSection sample={data.sample} />}
          <Distributions detail={data} />
          <OccurrencesSection occurrences={data.occurrences} />
        </div>
      )}
    </div>
  )
}

function BackLink() {
  return (
    <Link to="/crashes" className="text-sm text-blue-600 hover:underline">
      ← Crashes
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

function Badge({ children, className }: { children: ReactNode; className: string }) {
  return <span className={`inline-block text-xs px-1.5 py-0.5 rounded ${className}`}>{children}</span>
}

function Summary({ detail: d }: { detail: Detail }) {
  const s = d.sample
  return (
    <div className="bg-white rounded-lg shadow p-6">
      <div className="flex flex-wrap items-center gap-2">
        <Badge className="bg-red-100 text-red-800 font-mono">{d.crash_type}</Badge>
        <span className="text-sm font-semibold text-gray-800">{crashTypeLabels[d.crash_type] ?? d.crash_type}</span>
      </div>
      <p className="font-mono text-sm text-gray-900 whitespace-pre-wrap break-all mt-2">{s?.summary || '(no summary: a crash stored before APM-7)'}</p>
      <p className="text-xs text-gray-400 mt-1 font-mono break-all">
        {d.fingerprint}
        {d.fingerprint_version === 0 && ' (grouped by the SDK)'}
      </p>
      <div className="mt-4">
        {/* APM-8: resolve / reopen the group */}
        <IssueStatusControl key={d.fingerprint} kind="crash" fingerprint={d.fingerprint} info={d} />
      </div>
      <div className="grid grid-cols-2 md:grid-cols-4 gap-3 mt-4">
        <Stat label="Crashes" value={formatNumber(d.count)} />
        <Stat label="Sessions" value={formatNumber(d.session_count)} />
        <Stat label="First seen" value={formatDateTime(d.first_seen)} />
        <Stat label="Last seen" value={formatDateTime(d.last_seen)} />
      </div>
    </div>
  )
}

// Memory in KB as MB (formatNumber would write 4.6K MB for 4.5 GB).
function kb(v?: number): string {
  if (!v) return '-'
  return v >= 1024 ? `${Math.round(v / 1024).toLocaleString('en-US')} MB` : `${formatNumber(v)} KB`
}

function ExitSection({ sample: s }: { sample: CrashOccurrence }) {
  const rows: [string, ReactNode][] = []
  if (s.exit_reason) rows.push(['Exit reason (Android)', <span className="font-mono">{s.exit_reason}{s.exit_status ? ` · status ${s.exit_status}` : ''}</span>])
  if (s.exit_description) rows.push(['Description', <span className="font-mono break-all">{s.exit_description}</span>])
  if (s.signal) rows.push(['Signal', <span className="font-mono">{s.signal}{s.signal_code ? ` (${s.signal_code})` : ''}{s.fault_addr ? ` · fault addr ${s.fault_addr}` : ''}</span>])
  if (s.abort_message) rows.push(['Abort message', <span className="font-mono whitespace-pre-wrap break-all">{s.abort_message}</span>])
  rows.push(['In the foreground', s.foreground ? 'yes' : 'no'])
  if (s.pss_kb || s.rss_kb) rows.push(['Memory at the end', `PSS ${kb(s.pss_kb)} · RSS ${kb(s.rss_kb)}`])
  if (s.importance) rows.push(['Process importance', String(s.importance)])
  if (s.session_ms) rows.push(['Into the session', formatSessionMs(s.session_ms)])
  if (s.abi) rows.push(['ABI', s.abi])
  return (
    <div className="bg-white rounded-lg shadow p-6">
      <h3 className="text-sm font-medium text-gray-700 mb-1">How the process ended</h3>
      <p className="text-xs text-gray-500 mb-3">
        Of the latest crash ({formatDateTime(s.timestamp)}), reported by the SDK at the next launch
        {s.details?.detected_by === 'journal'
          ? ': Android kept no exit record (before Android 11, or none left), only the SDK saw that the game ended in the foreground.'
          : ' from Android’s record of the exit.'}
      </p>
      <table className="text-xs">
        <tbody>
          {rows.map(([k, v]) => (
            <tr key={k}>
              <td className="pr-4 py-0.5 text-gray-500 align-top whitespace-nowrap">{k}</td>
              <td className="py-0.5 text-gray-800">{v}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// A details value the SDK wrote as a number (game-sent text: parsed
// defensively; anything else is not shown as a number).
export function detailNumber(details: Record<string, string> | undefined, key: string): number | undefined {
  const raw = details?.[key]
  if (raw === undefined || raw === '' || raw.length > 32) return undefined
  const n = Number(raw)
  return Number.isFinite(n) ? n : undefined
}

function mbText(v: number | undefined): string {
  if (v === undefined) return '-'
  return v >= 1024 ? `${(v / 1024).toFixed(2)} GB` : `${v.toFixed(0)} MB`
}

// [last sample key, run peak key, label] as the SDK writes them into details.
const memoryFigures: [string, string, string][] = [
  ['mem_rss_mb', 'run_peak_rss_mb', 'Resident (RSS)'],
  ['mem_alloc_mb', 'run_peak_alloc_mb', 'Native allocated'],
  ['mem_reserved_mb', 'run_peak_reserved_mb', 'Native reserved'],
  ['mem_gfx_mb', 'run_peak_gfx_mb', 'Graphics'],
  ['mem_mb', 'run_peak_mem_mb', 'Managed used'],
  ['mem_mono_heap_mb', 'run_peak_mono_heap_mb', 'Managed heap'],
]

// APM-9: the last memory sample before the process died, the peak of the
// game run it was in and of the process, and Android's figures at the exit.
// Shown when the crash carries any of them (SDK with APM-9).
function MemorySection({ sample: s }: { sample: CrashOccurrence }) {
  const d = s.details
  const last = memoryFigures.map(([k, , label]) => [label, detailNumber(d, k)] as const)
  const peak = memoryFigures.map(([, k, label]) => [label, detailNumber(d, k)] as const)
  const hasLast = last.some(([, v]) => v !== undefined)
  const hasPeak = peak.some(([, v]) => v !== undefined)
  const procPeak = detailNumber(d, 'proc_peak_rss_mb')
  const age = detailNumber(d, 'mem_age_ms')
  const runMs = detailNumber(d, 'run_ms')
  const runId = d?.run_id ?? ''
  const android = !!(s.pss_kb || s.rss_kb)
  if (!hasLast && !hasPeak && procPeak === undefined && !runId) return null
  const lowMemory = s.crash_type === 'oom' || s.crash_type === 'killed'
  return (
    <div className="bg-white rounded-lg shadow p-6" data-testid="memory-before-death">
      <h3 className="text-sm font-medium text-gray-700 mb-1">Memory before death</h3>
      <p className="text-xs text-gray-500 mb-3">
        {lowMemory
          ? 'The process was killed: what the SDK last recorded, and how high memory went in the game run and the process.'
          : 'What the SDK last recorded before the crash.'}
      </p>
      <table className="text-xs">
        <thead className="text-gray-500">
          <tr>
            <th className="pr-4 text-left font-normal" />
            {hasLast && <th className="pr-4 text-right font-normal">Last sample{age !== undefined && age > 0 ? ` (${formatSessionMs(age)} before)` : ''}</th>}
            {hasPeak && <th className="pr-4 text-right font-normal">Peak of the run</th>}
          </tr>
        </thead>
        <tbody>
          {memoryFigures.map(([, , label], i) =>
            last[i][1] === undefined && peak[i][1] === undefined ? null : (
              <tr key={label}>
                <td className="pr-4 py-0.5 text-gray-500 whitespace-nowrap">{label}</td>
                {hasLast && <td className="pr-4 py-0.5 text-right text-gray-800">{mbText(last[i][1])}</td>}
                {hasPeak && <td className="pr-4 py-0.5 text-right text-gray-800">{mbText(peak[i][1])}</td>}
              </tr>
            )
          )}
        </tbody>
      </table>
      <div className="text-xs text-gray-600 mt-2 space-y-0.5">
        {procPeak !== undefined && <p>Peak resident memory of the process: {mbText(procPeak)}</p>}
        {android && (
          <p>
            Android at the exit: PSS {kb(s.pss_kb)} · RSS {kb(s.rss_kb)}
          </p>
        )}
        {runId && (
          <p className="break-all">
            Game run <span className="font-mono">{runId.length > 120 ? runId.slice(0, 120) + '…' : runId}</span>
            {runMs !== undefined ? `, ${formatSessionMs(runMs)} in` : ''}
          </p>
        )}
      </div>
    </div>
  )
}

function SymbolStatus({ symbols }: { symbols: CrashSymbolStatus[] }) {
  if (symbols.length === 0) return null
  const missing = symbols.filter((s) => !s.found)
  return (
    <div className="mb-3 space-y-1" data-testid="symbol-status">
      {symbols.map((s) => (
        <p key={s.module + s.build_id} className="text-xs">
          <span className="font-mono">{s.module}</span>{' '}
          {s.build_id && <span className="font-mono text-gray-400">{s.build_id}</span>}{' '}
          {s.found ? (
            <Badge className="bg-green-100 text-green-800">
              symbols uploaded{s.uploaded_at ? ` ${formatDateTime(s.uploaded_at)}` : ''}
              {s.line_map ? ', with C# lines' : ', no C# lines'}
            </Badge>
          ) : (
            <Badge className="bg-amber-100 text-amber-800">no symbols uploaded</Badge>
          )}
        </p>
      ))}
      {missing.length > 0 && (
        <p className="text-xs text-gray-500">
          Frames of a library without symbols show its address only. Upload the build&apos;s symbols with{' '}
          <span className="font-mono">apmsym upload</span> (RELEASE.md, APM-7); they apply to crashes already stored.
        </p>
      )}
    </div>
  )
}

function FrameRow({ f }: { f: CrashFrame }) {
  return (
    <li className={f.in_app ? 'text-gray-900' : 'text-gray-400'} title={f.raw}>
      <span className="text-gray-400 mr-2">#{String(f.index).padStart(2, '0')}</span>
      <span className={f.in_app ? 'font-semibold' : ''}>{frameLabel(f)}</span>
      {f.file && (
        <span className={f.in_app ? 'text-blue-700' : ''}>
          {' '}
          ({f.file}
          {f.line ? `:${f.line}` : ''})
        </span>
      )}
      {f.native && f.function && (
        <span className="text-gray-400">
          {' '}
          · {f.module}
          {f.pc ? ` ${f.pc}` : ''}
        </span>
      )}
      {f.symbolicated && f.symbols_by === 'app_version' && (
        <span className="text-amber-700" title="The frame had no build id: named with the newest upload of this version and ABI">
          {' '}
          (by version)
        </span>
      )}
    </li>
  )
}

function ThreadView({ t }: { t: CrashThread }) {
  return (
    <div>
      <p className="text-xs font-semibold text-gray-800">
        Thread <span className="font-mono">{t.name || '(unnamed)'}</span>
        {t.tid ? <span className="font-normal text-gray-500"> tid {t.tid}</span> : null}
        {t.crashed && <Badge className="ml-2 bg-red-100 text-red-800">crashed / blocked</Badge>}
      </p>
      {t.frames.length === 0 ? (
        <p className="text-xs text-gray-400 pl-4">No frames.</p>
      ) : (
        <ol className="font-mono text-xs space-y-0.5 pl-4">
          {t.frames.map((f) => (
            <FrameRow key={f.index} f={f} />
          ))}
        </ol>
      )}
      {t.omitted > 0 && <p className="text-xs text-gray-400 pl-4">{t.omitted} more frames</p>}
    </div>
  )
}

function StackSection({ detail: d }: { detail: Detail }) {
  const s = d.sample
  const threads = s?.threads ?? []
  return (
    <div className="bg-white rounded-lg shadow p-6">
      <h3 className="text-sm font-medium text-gray-700 mb-1">Stack</h3>
      <p className="text-xs text-gray-500 mb-3">
        The crashed (or, for an ANR, the blocked) thread first, then UnityMain, where the game&apos;s C# code runs. Frames of
        the game&apos;s libraries are dark; their names, files and lines come from the uploaded symbols when the crash is read.
      </p>
      <SymbolStatus symbols={d.symbols ?? []} />
      {threads.length > 0 ? (
        <div className="space-y-4">
          {threads.map((t, i) => (
            <ThreadView key={i} t={t} />
          ))}
        </div>
      ) : (
        <p className="text-xs text-gray-500">
          {d.crash_type === 'oom' || d.crash_type === 'killed' || d.crash_type === 'unknown'
            ? 'No stack: the process was killed, not crashed (Android keeps no stack for a kill).'
            : 'No stack was sent (Android 11 keeps none for a native crash; a Java crash has none).'}
        </p>
      )}
      {(s?.stack || d.stack) && (
        <details className="mt-3">
          <summary className="text-xs text-gray-500 cursor-pointer">Stack as sent</summary>
          <pre className="text-xs bg-gray-50 p-2 rounded overflow-auto max-h-64 font-mono whitespace-pre-wrap break-all mt-2">
            {s?.stack || d.stack}
          </pre>
        </details>
      )}
    </div>
  )
}

function ContextSection({ sample: s }: { sample: CrashOccurrence }) {
  const ctx = Object.entries(s.context ?? {}).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
  const details = Object.entries(s.details ?? {}).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
  return (
    <div className="bg-white rounded-lg shadow p-6">
      <h3 className="text-sm font-medium text-gray-700 mb-1">Before the crash</h3>
      <p className="text-xs text-gray-500 mb-3">
        The game context, scene and time line the SDK had on disk when the process ended (written within 0.1 s of a change).
      </p>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-x-6 gap-y-1 text-xs text-gray-700 mb-3">
        <p>
          <span className="text-gray-500">Version:</span> {s.app_version || '-'}
          {s.build_type ? ` (${s.build_type})` : ''}
        </p>
        <p>
          <span className="text-gray-500">Device:</span> {s.device_model || '-'} · {s.platform || '-'} {s.os_version}
        </p>
        <p>
          <span className="text-gray-500">Scene:</span> {s.scene || '-'}
        </p>
        <SessionLinks o={s} />
      </div>
      {ctx.length > 0 ? (
        <table className="mb-3 text-xs font-mono" aria-label="Game context">
          <tbody>
            {ctx.map(([k, v]) => (
              <tr key={k}>
                <td className="pr-3 text-gray-500 align-top">{k}</td>
                <td className="text-gray-800 break-all">{v}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <p className="text-xs text-gray-400 mb-3">No game context.</p>
      )}
      <BreadcrumbList lines={s.breadcrumbs ?? []} endLabel={(s.breadcrumbs ?? []).length > 0 ? 'The crash' : undefined} />
      {details.length > 0 && (
        <details className="mt-3">
          <summary className="text-xs text-gray-500 cursor-pointer">Details ({details.length})</summary>
          <table className="mt-1 text-xs font-mono">
            <tbody>
              {details.map(([k, v]) => (
                <tr key={k}>
                  <td className="pr-3 text-gray-500 align-top">{k}</td>
                  <td className="text-gray-800 break-all">{v}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
    </div>
  )
}

function SessionLinks({ o }: { o: CrashOccurrence }) {
  if (!o.session_id) return null
  return (
    <>
      <p className="break-all">
        <span className="text-gray-500">Session:</span> <span className="font-mono">{o.session_id}</span>{' '}
        <Link
          to={sessionLink({ session_id: o.session_id, timestamp: o.timestamp, session_ms: o.session_ms ?? 0 })}
          className="text-blue-600 hover:underline"
        >
          Session time line at the crash
        </Link>
      </p>
      {o.device_id && (
        <p className="break-all">
          <span className="text-gray-500">Device id:</span> <span className="font-mono">{o.device_id}</span>{' '}
          <Link to={deviceSessionsLink(o.device_id, o.timestamp)} className="text-blue-600 hover:underline">
            Its sessions
          </Link>
        </p>
      )}
    </>
  )
}

function Distributions({ detail: d }: { detail: Detail }) {
  const lists: [string, { label: string; count: number }[]][] = [
    ['Versions', d.version_distribution.map((v) => ({ label: v.version, count: v.count }))],
    ['Devices', d.device_distribution.map((v) => ({ label: v.device, count: v.count }))],
  ]
  return (
    <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
      {lists.map(([title, rows]) => (
        <div key={title} className="bg-white rounded-lg shadow p-6">
          <h3 className="text-sm font-medium text-gray-700 mb-2">{title}</h3>
          {rows.length === 0 ? (
            <p className="text-xs text-gray-400">None.</p>
          ) : (
            rows.slice(0, 10).map((r) => (
              <div key={r.label} className="flex justify-between text-xs">
                <span className="truncate font-mono">{r.label || '-'}</span>
                <span className="text-gray-500">{formatNumber(r.count)}</span>
              </div>
            ))
          )}
        </div>
      ))}
    </div>
  )
}

function OccurrencesSection({ occurrences }: { occurrences: CrashOccurrence[] }) {
  return (
    <div className="bg-white rounded-lg shadow p-6">
      <h3 className="text-sm font-medium text-gray-700 mb-3">Recent crashes</h3>
      {occurrences.length === 0 ? (
        <p className="text-sm text-gray-500">None.</p>
      ) : (
        <div className="space-y-3">
          {occurrences.map((o, i) => (
            <div key={o.event_id || i} className="border border-gray-200 rounded p-3 text-xs" data-testid="crash-occurrence">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium text-gray-900">{formatDateTime(o.timestamp)}</span>
                {o.exit_reason && <Badge className="bg-gray-100 text-gray-700 font-mono">{o.exit_reason}</Badge>}
                {o.foreground && <Badge className="bg-red-50 text-red-700">foreground</Badge>}
                <span className="text-gray-500">
                  {o.app_version} · {o.device_model} · {o.os_version} · {o.scene || '-'}
                </span>
              </div>
              {o.summary && <p className="font-mono text-gray-800 break-all mt-1">{o.summary}</p>}
              <div className="mt-1 text-gray-700">
                <SessionLinks o={o} />
              </div>
              {(o.breadcrumbs ?? []).length > 0 && (
                <details className="mt-1">
                  <summary className="text-gray-500 cursor-pointer">Breadcrumbs ({o.breadcrumbs.length})</summary>
                  <div className="mt-2">
                    <BreadcrumbList lines={o.breadcrumbs} endLabel="The crash" />
                  </div>
                </details>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
