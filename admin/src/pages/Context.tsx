import { useState } from 'react'
import { GlobalFilterBar } from '../components/FilterBar'
import { useFilters } from '../hooks/useFilters'
import { useContextKeys, useContextDistribution } from '../hooks/useApi'
import { formatNumber, formatMs } from '../utils/format'
import type { ContextMetric, ContextDistributionRow } from '../types/api'

const metrics: { value: ContextMetric; label: string }[] = [
  { value: 'fps', label: 'FPS' },
  { value: 'frame_time', label: 'Frame time' },
  { value: 'janks', label: 'Janks' },
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

  const { data: keyData, isLoading: keysLoading, isError: keysError } = useContextKeys(queryParams)
  const keys = keyData?.keys ?? []
  // Until a key is picked, show the most common one.
  const key = selectedKey || keys[0]?.key || ''

  const { data: dist, isLoading: distLoading, isError: distError } = useContextDistribution(key, metric, queryParams)
  const rows: ContextDistributionRow[] = dist?.rows ?? []
  const total = dist?.total ?? 0
  const unit = dist?.unit ?? ''
  const maxCount = rows.reduce((m, r) => Math.max(m, r.count), 0)

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
                <th className="px-4 py-2 text-right">Count</th>
                <th className="px-4 py-2 text-left w-1/4">Share</th>
                <th className="px-4 py-2 text-right">Sessions</th>
                {unit && <th className="px-4 py-2 text-right">p50</th>}
                {unit && <th className="px-4 py-2 text-right">p95</th>}
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const share = total > 0 ? (r.count / total) * 100 : 0
                const width = maxCount > 0 ? (r.count / maxCount) * 100 : 0
                return (
                  <tr key={r.value} className="border-t border-gray-100">
                    <td className="px-4 py-2 font-mono">
                      {r.value === '' ? <span className="text-gray-400">(not set)</span> : r.value}
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
                    {unit && <td className="px-4 py-2 text-right">{formatValue(r.p50, unit)}</td>}
                    {unit && <td className="px-4 py-2 text-right">{formatValue(r.p95, unit)}</td>}
                    <td className="px-4 py-2 text-right">
                      <button
                        onClick={() => addContextFilter({ key, value: r.value })}
                        className="text-xs text-blue-600 hover:underline"
                      >
                        Filter
                      </button>
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
