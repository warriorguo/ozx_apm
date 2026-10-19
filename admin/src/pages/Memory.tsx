import { useState } from 'react'
import { GlobalFilterBar } from '../components/FilterBar'
import { Clipped, GroupPicker, groupLabel } from '../components/GroupPicker'
import { useFilters } from '../hooks/useFilters'
import { useMemoryBreakdown, useRunPeaks } from '../hooks/useApi'
import { formatMs, formatNumber, requestError } from '../utils/format'
import type { AnalysisColumn, AnalysisGroup, MemoryFigureName, RunPeakMetric, RunPeakBucket } from '../types/api'

const figures: { name: MemoryFigureName; label: string; hint: string }[] = [
  { name: 'mem_mb', label: 'Managed used', hint: 'Managed (Mono/IL2CPP) heap in use (GC.GetTotalMemory)' },
  { name: 'mem_mono_heap_mb', label: 'Managed heap', hint: 'Managed heap reserved by the runtime' },
  { name: 'mem_alloc_mb', label: 'Native allocated', hint: "Unity's native memory allocated (Profiler.GetTotalAllocatedMemoryLong)" },
  { name: 'mem_reserved_mb', label: 'Native reserved', hint: "Unity's native memory reserved (Profiler.GetTotalReservedMemoryLong)" },
  { name: 'mem_gfx_mb', label: 'Graphics', hint: 'Graphics memory Unity estimates (Gfx Used Memory)' },
  { name: 'mem_rss_mb', label: 'Resident (RSS)', hint: "The process's resident memory (Android: RSS; elsewhere Unity's System Used Memory)" },
]

const peakMetrics: { value: RunPeakMetric; label: string }[] = [
  { value: 'peak_rss', label: 'Resident (RSS)' },
  { value: 'peak_alloc', label: 'Native allocated' },
  { value: 'peak_reserved', label: 'Native reserved' },
  { value: 'peak_gfx', label: 'Graphics' },
  { value: 'peak_mem', label: 'Managed used' },
  { value: 'peak_mono_heap', label: 'Managed heap' },
]

const breakdownColumns: AnalysisColumn[] = ['app_version', 'build_type', 'device_model', 'ram_class', 'platform']

export function mb(v: number | undefined): string {
  if (!v || !Number.isFinite(v)) return '-'
  return v >= 1024 ? `${(v / 1024).toFixed(2)} GB` : `${v.toFixed(0)} MB`
}

function bucketLabel(b: RunPeakBucket): string {
  return b.high ? `${b.low ? mb(b.low) : '0'} – ${mb(b.high)}` : `≥ ${mb(b.low)}`
}

// Memory (APM-9): the perf samples' memory breakdown and the peak memory of
// game runs (ApmClient.BeginRun / EndRun), with the runs the process did not
// survive (killed for low memory, crashed).
export function Memory() {
  const { queryParams } = useFilters()
  const [group, setGroup] = useState<AnalysisGroup>({ kind: 'by', key: 'app_version' })
  const [metric, setMetric] = useState<RunPeakMetric>('peak_rss')
  const breakdown = useMemoryBreakdown(group, queryParams)
  const peaks = useRunPeaks(metric, group, queryParams)

  const rows = breakdown.data?.rows ?? []
  const peakRows = peaks.data?.rows ?? []
  const buckets = peaks.data?.buckets ?? []
  const maxBucket = buckets.reduce((m, b) => Math.max(m, b.runs), 0)

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Memory</h1>
      <GlobalFilterBar />
      <div className="bg-white rounded-lg shadow p-4 mb-6 flex flex-wrap items-center gap-4">
        <GroupPicker value={group} onChange={setGroup} columns={breakdownColumns} params={queryParams} />
        <span className="text-xs text-gray-400">The range may be at most 31 days.</span>
      </div>

      <section className="bg-white rounded-lg shadow mb-6">
        <h2 className="text-sm font-medium text-gray-700 px-4 pt-4">Memory breakdown (perf samples)</h2>
        <p className="text-xs text-gray-500 px-4 pb-2">
          p50 / p95 / max per figure over the samples that have it; samples of SDKs before APM-9 have only the managed heap.
        </p>
        {breakdown.isLoading ? (
          <div className="text-center py-6 text-gray-500">Loading...</div>
        ) : breakdown.isError ? (
          <div className="text-center py-6 text-red-500">{requestError(breakdown.error, 'Could not load the memory breakdown.')}</div>
        ) : rows.length === 0 ? (
          <div className="text-center py-6 text-gray-500 text-sm">No perf samples in this range.</div>
        ) : (
          <div className="overflow-x-auto">
            <table className="min-w-full text-sm">
              <thead className="bg-gray-50 text-gray-500 text-xs">
                <tr>
                  <th className="px-3 py-2 text-left">{groupLabel(group)}</th>
                  <th className="px-3 py-2 text-right">Samples</th>
                  {figures.map((f) => (
                    <th key={f.name} className="px-3 py-2 text-right" title={f.hint}>
                      {f.label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.value} className="border-t border-gray-100 align-top">
                    <td className="px-3 py-2 font-mono break-all">
                      <Clipped text={r.value} />
                    </td>
                    <td className="px-3 py-2 text-right">
                      {formatNumber(r.samples)}
                      <div className="text-xs text-gray-400">{formatNumber(r.sessions)} sessions</div>
                    </td>
                    {figures.map((f) => {
                      const v = r.figures[f.name]
                      return (
                        <td key={f.name} className="px-3 py-2 text-right whitespace-nowrap" data-testid={`fig-${r.value}-${f.name}`}>
                          {v && v.rows > 0 ? (
                            <>
                              {mb(v.p50)} / {mb(v.p95)}
                              <div className="text-xs text-gray-400">
                                max {mb(v.max)} · {formatNumber(v.rows)}
                              </div>
                            </>
                          ) : (
                            <span className="text-gray-300">-</span>
                          )}
                        </td>
                      )
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
            {breakdown.data?.truncated && <p className="px-4 py-2 text-xs text-gray-500">Showing the {rows.length} largest groups.</p>}
          </div>
        )}
      </section>

      <section className="bg-white rounded-lg shadow">
        <div className="flex flex-wrap items-center gap-4 px-4 pt-4">
          <h2 className="text-sm font-medium text-gray-700">Peak memory per run</h2>
          <label className="flex items-center gap-2 text-sm text-gray-500">
            Peak of:
            <select
              aria-label="Peak metric"
              value={metric}
              onChange={(e) => setMetric(e.target.value as RunPeakMetric)}
              className="px-3 py-1.5 text-sm border border-gray-300 rounded text-gray-900"
            >
              {peakMetrics.map((m) => (
                <option key={m.value} value={m.value}>
                  {m.label}
                </option>
              ))}
            </select>
          </label>
        </div>
        <p className="text-xs text-gray-500 px-4 pb-2">
          A run is what the game marks with ApmClient.BeginRun / EndRun. Runs the process did not survive are reported at the next
          launch (crashed; OOM = killed for low memory). Growth: end minus start of the runs that ended normally.
        </p>
        {peaks.isLoading ? (
          <div className="text-center py-6 text-gray-500">Loading...</div>
        ) : peaks.isError ? (
          <div className="text-center py-6 text-red-500">{requestError(peaks.error, 'Could not load the run peaks.')}</div>
        ) : peakRows.length === 0 ? (
          <div className="text-center py-6 text-gray-500 text-sm">
            No runs in this range. Runs are sent once the game calls ApmClient.BeginRun / EndRun (SDK with APM-9).
          </div>
        ) : (
          <>
            <div className="overflow-x-auto">
              <table className="min-w-full text-sm">
                <thead className="bg-gray-50 text-gray-500 text-xs">
                  <tr>
                    <th className="px-3 py-2 text-left">{groupLabel(group)}</th>
                    <th className="px-3 py-2 text-right">Runs</th>
                    <th className="px-3 py-2 text-right">p50</th>
                    <th className="px-3 py-2 text-right">p90</th>
                    <th className="px-3 py-2 text-right">p99</th>
                    <th className="px-3 py-2 text-right">Max</th>
                    <th className="px-3 py-2 text-right">Crashed</th>
                    <th className="px-3 py-2 text-right">OOM</th>
                    <th className="px-3 py-2 text-right">Killed</th>
                    <th className="px-3 py-2 text-right">Avg length</th>
                    <th className="px-3 py-2 text-right">Avg growth</th>
                  </tr>
                </thead>
                <tbody>
                  {peakRows.map((r) => (
                    <tr key={r.value} className="border-t border-gray-100">
                      <td className="px-3 py-2 font-mono break-all">
                        <Clipped text={r.value} />
                      </td>
                      <td className="px-3 py-2 text-right" title={`${r.with_value} with this figure, ${r.sessions} sessions, ${r.devices} devices`}>
                        {formatNumber(r.runs)}
                      </td>
                      <td className="px-3 py-2 text-right">{mb(r.p50)}</td>
                      <td className="px-3 py-2 text-right">{mb(r.p90)}</td>
                      <td className="px-3 py-2 text-right">{mb(r.p99)}</td>
                      <td className="px-3 py-2 text-right">{mb(r.max)}</td>
                      <td className="px-3 py-2 text-right">{formatNumber(r.crashed_runs ?? 0)}</td>
                      <td className={`px-3 py-2 text-right ${r.oom_runs ? 'text-red-600 font-medium' : ''}`}>{formatNumber(r.oom_runs ?? 0)}</td>
                      <td className="px-3 py-2 text-right">{formatNumber(r.killed_runs ?? 0)}</td>
                      <td className="px-3 py-2 text-right">{formatMs(r.avg_duration_ms ?? 0)}</td>
                      <td className="px-3 py-2 text-right">{r.avg_growth_mb ? `${r.avg_growth_mb > 0 ? '+' : ''}${r.avg_growth_mb.toFixed(0)} MB` : '-'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {buckets.length > 0 && (
              <div className="px-4 py-4">
                <h3 className="text-xs font-medium text-gray-600 mb-2">Distribution of the peak (all filtered runs)</h3>
                <div className="space-y-1">
                  {buckets.map((b) => (
                    <div key={b.low} className="flex items-center gap-2 text-xs" data-testid="peak-bucket">
                      <span className="w-36 text-right text-gray-600">{bucketLabel(b)}</span>
                      <div className="flex-1 h-3 bg-gray-100 rounded relative">
                        <div className="h-3 bg-blue-500 rounded" style={{ width: `${maxBucket ? (b.runs / maxBucket) * 100 : 0}%` }} />
                        {b.oom > 0 && (
                          <div
                            className="h-3 bg-red-500 rounded absolute top-0 left-0"
                            style={{ width: `${maxBucket ? (b.oom / maxBucket) * 100 : 0}%` }}
                          />
                        )}
                      </div>
                      <span className="w-28 text-gray-600">
                        {formatNumber(b.runs)} runs{b.oom > 0 && <span className="text-red-600"> · {b.oom} OOM</span>}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </>
        )}
      </section>
    </div>
  )
}
