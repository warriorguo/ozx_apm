import { useState } from 'react'
import { GlobalFilterBar } from '../components/FilterBar'
import { useFilters } from '../hooks/useFilters'
import { useContextKeys, useContextDistribution } from '../hooks/useApi'
import { formatNumber, formatMs } from '../utils/format'
import { Clipped } from '../components/GroupPicker'
import type { ContextMetric, ContextDistributionRow } from '../types/api'

const metrics: { value: ContextMetric; label: string }[] = [
  { value: 'fps', label: 'FPS' },
  { value: 'frame_time', label: 'Frame time' },
  { value: 'janks', label: 'Janks' },
  { value: 'jank_rate', label: 'Janks per minute of play' },
  { value: 'exceptions', label: 'Exceptions' },
  { value: 'crashes', label: 'Crashes' },
  { value: 'startup', label: 'Startup time' },
  { value: 'scene_load', label: 'Scene load time' },
]

function formatValue(v: number, unit: string): string {
  if (unit === 'ms') return formatMs(v)
  if (unit === 'fps') return v.toFixed(1)
  return ''
}

// Distribution of one metric by the value of a game context key (APM-5),
// e.g. FPS p50/p95 per stageType, or exceptions per roomId.
export function Context() {
  const { queryParams, addContextFilter } = useFilters()
  const [selectedKey, setSelectedKey] = useState('')
  const [metric, setMetric] = useState<ContextMetric>('fps')
  // APM-9: numeric values (e.g. alive enemies) grouped into count ranges.
  const [bucket, setBucket] = useState(false)

  const { data: keyData, isLoading: keysLoading, isError: keysError } = useContextKeys(queryParams)
  const keys = keyData?.keys ?? []
  // Until a key is picked, show the most common one.
  const key = selectedKey || keys[0]?.key || ''

  const { data: dist, isLoading: distLoading, isError: distError } = useContextDistribution(
    key,
    metric,
    bucket ? { ...queryParams, bucket: 'count' } : queryParams
  )
  const rows: ContextDistributionRow[] = dist?.rows ?? []
  const total = dist?.total ?? 0
  const unit = dist?.unit ?? ''
  const maxCount = rows.reduce((m, r) => Math.max(m, r.count), 0)
  const isRate = metric === 'jank_rate'
  // p05 / p99 from servers with APM-9 (absent before); for fps p05 is the slow side.
  const hasTails = unit !== '' && !isRate && rows.some((r) => r.p05 !== undefined)

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Distribution by Context</h1>

      <GlobalFilterBar />

      <div className="bg-white rounded-lg shadow p-4 mb-6 flex flex-wrap items-center gap-4">
        <div className="flex items-center gap-2">
          <span className="text-sm text-gray-500">Group by key:</span>
          <select
            aria-label="Context key"
            value={key}
            onChange={(e) => setSelectedKey(e.target.value)}
            disabled={keys.length === 0}
            className="px-3 py-1.5 text-sm border border-gray-300 rounded font-mono"
          >
            {keys.length === 0 && <option value="">(no keys)</option>}
            {keys.map((k) => (
              <option key={k.key} value={k.key}>
                {k.key} ({formatNumber(k.count)})
              </option>
            ))}
          </select>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-sm text-gray-500">Metric:</span>
          <select
            aria-label="Metric"
            value={metric}
            onChange={(e) => setMetric(e.target.value as ContextMetric)}
            className="px-3 py-1.5 text-sm border border-gray-300 rounded"
          >
            {metrics.map((m) => (
              <option key={m.value} value={m.value}>
                {m.label}
              </option>
            ))}
          </select>
        </div>
        <label className="flex items-center gap-2 text-sm text-gray-500" title="E.g. an alive enemy count: 0, 1-5, 6-10, 11-20, 21-30, 31-50, 51-100, 101-200, 201+">
          <input type="checkbox" checked={bucket} onChange={(e) => setBucket(e.target.checked)} aria-label="Group numeric values into count ranges" />
          Group numeric values into count ranges
        </label>
      </div>

      {keysLoading ? (
        <div className="text-center py-8 text-gray-500">Loading...</div>
      ) : keysError ? (
        <div className="text-center py-8 text-red-500">Could not load the context keys.</div>
      ) : keys.length === 0 ? (
        <div className="bg-white rounded-lg shadow p-6 text-center text-gray-500 text-sm">
          No game context in this range. Events carry a context once the game calls
          ApmClient.SetContext (SDK with APM-5); older events have none.
        </div>
      ) : distLoading ? (
        <div className="text-center py-8 text-gray-500">Loading...</div>
      ) : distError ? (
        <div className="text-center py-8 text-red-500">Could not load the distribution.</div>
      ) : rows.length === 0 ? (
        <div className="bg-white rounded-lg shadow p-6 text-center text-gray-500 text-sm">No data</div>
      ) : (
        <div className="bg-white rounded-lg shadow overflow-x-auto">
          <table className="min-w-full text-sm">
            <thead className="bg-gray-50 text-gray-500 text-xs uppercase">
              <tr>
                <th className="px-4 py-2 text-left">{key}</th>
                <th className="px-4 py-2 text-right">{isRate ? 'Janks' : 'Count'}</th>
                <th className="px-4 py-2 text-left w-1/4">Share</th>
                <th className="px-4 py-2 text-right">Sessions</th>
                {isRate && <th className="px-4 py-2 text-right">Minutes played</th>}
                {isRate && <th className="px-4 py-2 text-right">Janks / min</th>}
                {hasTails && <th className="px-4 py-2 text-right" title={unit === 'fps' ? 'The slowest 5%' : undefined}>p05</th>}
                {unit && !isRate && <th className="px-4 py-2 text-right">p50</th>}
                {unit && !isRate && <th className="px-4 py-2 text-right">p95</th>}
                {hasTails && <th className="px-4 py-2 text-right">p99</th>}
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const share = total > 0 ? (r.count / total) * 100 : 0
                const width = maxCount > 0 ? (r.count / maxCount) * 100 : 0
                return (
                  <tr key={r.value} className="border-t border-gray-100">
                    <td className="px-4 py-2 font-mono break-all">
                      <Clipped text={r.value} />
                    </td>
                    <td className="px-4 py-2 text-right">{formatNumber(r.count)}</td>
                    <td className="px-4 py-2">
                      <div className="flex items-center gap-2">
                        <div className="h-2 bg-gray-100 rounded flex-1">
                          <div className="h-2 bg-blue-500 rounded" style={{ width: `${width}%` }} />
                        </div>
                        <span className="text-xs text-gray-500 w-12 text-right">{share.toFixed(1)}%</span>
                      </div>
                    </td>
                    <td className="px-4 py-2 text-right">{formatNumber(r.sessions)}</td>
                    {isRate && <td className="px-4 py-2 text-right">{(r.minutes ?? 0).toFixed(1)}</td>}
                    {isRate && <td className="px-4 py-2 text-right">{r.rate == null ? "-" : r.rate.toFixed(2)}</td>}
                    {hasTails && <td className="px-4 py-2 text-right">{formatValue(r.p05 ?? 0, unit)}</td>}
                    {unit && !isRate && <td className="px-4 py-2 text-right">{formatValue(r.p50, unit)}</td>}
                    {unit && !isRate && <td className="px-4 py-2 text-right">{formatValue(r.p95, unit)}</td>}
                    {hasTails && <td className="px-4 py-2 text-right">{formatValue(r.p99 ?? 0, unit)}</td>}
                    <td className="px-4 py-2 text-right">
                      {/* A count range is not a stored value: no filter for it. */}
                      {!(dist?.bucket && /^(\d+-\d+|\d+\+)$/.test(r.value)) && (
                      <button
                        onClick={() => addContextFilter({ key, value: r.value })}
                        className="text-xs text-blue-600 hover:underline"
                      >
                        Filter
                      </button>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
          {dist?.truncated && (
            <p className="px-4 py-2 text-xs text-gray-500">
              Showing the {rows.length} most frequent values.
            </p>
          )}
        </div>
      )}
    </div>
  )
}
