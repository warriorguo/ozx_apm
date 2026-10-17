import { useState } from 'react'
import { GlobalFilterBar } from '../components/FilterBar'
import { useFilters } from '../hooks/useFilters'
import { useContextKeys, useCustomDistribution, useCustomEvents, useCustomMetrics } from '../hooks/useApi'
import { formatDateTime, formatMs, formatNumber } from '../utils/format'
import type { CustomColumn, CustomGroup, CustomMetricStat } from '../types/api'

const columns: { value: CustomColumn; label: string }[] = [
  { value: 'app_version', label: 'App version' },
  { value: 'platform', label: 'Platform' },
  { value: 'device_model', label: 'Device' },
  { value: 'os_version', label: 'OS version' },
  { value: 'build_type', label: 'Build type' },
]

// A metric value in its unit: milliseconds as ms/s, others as numbers.
export function formatMetric(v: number, unit: string): string {
  if (unit === 'ms') return v < 10 ? v.toFixed(1) + 'ms' : formatMs(v)
  const n = Math.abs(v) >= 100 ? v.toFixed(0) : Math.abs(v) >= 1 ? v.toFixed(2) : v.toPrecision(3)
  return unit ? `${n} ${unit}` : n
}

// Encodes a group as one select value: "key:roomId", "prop:seed", "by:app_version".
function groupValue(g: CustomGroup): string {
  return `${g.kind}:${g.key}`
}

function parseGroup(v: string): CustomGroup {
  const i = v.indexOf(':')
  const kind = v.slice(0, i)
  const key = v.slice(i + 1)
  if (kind === 'key' || kind === 'prop') return { kind, key }
  return { kind: 'by', key: key as CustomColumn }
}

function GroupSelect({
  value,
  onChange,
  contextKeys,
  allowProps,
}: {
  value: CustomGroup
  onChange: (g: CustomGroup) => void
  contextKeys: string[]
  allowProps: boolean
}) {
  const [prop, setProp] = useState(value.kind === 'prop' ? value.key : '')
  return (
    <div className="flex items-center gap-2">
      <span className="text-sm text-gray-500">Group by:</span>
      <select
        aria-label="Group by"
        value={value.kind === 'prop' ? 'prop:' : groupValue(value)}
        onChange={(e) => {
          const v = e.target.value
          if (v === 'prop:') onChange({ kind: 'prop', key: prop })
          else onChange(parseGroup(v))
        }}
        className="px-3 py-1.5 text-sm border border-gray-300 rounded"
      >
        <optgroup label="Game context">
          {contextKeys.map((k) => (
            <option key={k} value={`key:${k}`}>
              {k}
            </option>
          ))}
        </optgroup>
        <optgroup label="Device / build">
          {columns.map((c) => (
            <option key={c.value} value={`by:${c.value}`}>
              {c.label}
            </option>
          ))}
        </optgroup>
        {allowProps && (
          <optgroup label="Event property">
            <option value="prop:">Property…</option>
          </optgroup>
        )}
      </select>
      {value.kind === 'prop' && (
        <input
          aria-label="Property key"
          value={prop}
          placeholder="property key"
          onChange={(e) => {
            setProp(e.target.value)
            onChange({ kind: 'prop', key: e.target.value })
          }}
          className="px-3 py-1.5 text-sm border border-gray-300 rounded font-mono w-40"
        />
      )}
    </div>
  )
}

function ShareBar({ count, total, max }: { count: number; total: number; max: number }) {
  const share = total > 0 ? (count / total) * 100 : 0
  const width = max > 0 ? (count / max) * 100 : 0
  return (
    <div className="flex items-center gap-2">
      <div className="h-2 bg-gray-100 rounded flex-1">
        <div className="h-2 bg-blue-500 rounded" style={{ width: `${width}%` }} />
      </div>
      <span className="text-xs text-gray-500 w-12 text-right">{share.toFixed(1)}%</span>
    </div>
  )
}

function Distribution({ kind, name, contextKeys }: { kind: 'events' | 'metrics'; name: string; contextKeys: string[] }) {
  const { queryParams, addContextFilter } = useFilters()
  const [chosen, setGroup] = useState<CustomGroup | null>(null)
  // Until one is picked: roomId if the data has it, else the most common
  // context key, else the app version.
  const group: CustomGroup =
    chosen ??
    (contextKeys.includes('roomId')
      ? { kind: 'key', key: 'roomId' }
      : contextKeys[0]
        ? { kind: 'key', key: contextKeys[0] }
        : { kind: 'by', key: 'app_version' })
  const { data, isLoading, isError } = useCustomDistribution(kind, name, group, queryParams)
  const rows: CustomMetricStat[] = data?.rows ?? []
  const total = data?.total ?? 0
  const unit = data?.unit ?? ''
  const max = rows.reduce((m, r) => Math.max(m, r.count), 0)
  const metric = kind === 'metrics'

  return (
    <div className="bg-white rounded-lg shadow mt-6">
      <div className="p-4 flex flex-wrap items-center gap-4 border-b border-gray-100">
        <h2 className="font-semibold text-gray-900 font-mono">{name}</h2>
        <GroupSelect value={group} onChange={setGroup} contextKeys={contextKeys} allowProps={!metric} />
      </div>
      {isLoading ? (
        <div className="text-center py-8 text-gray-500">Loading...</div>
      ) : isError ? (
        <div className="text-center py-8 text-red-500">Could not load the distribution.</div>
      ) : rows.length === 0 ? (
        <div className="p-6 text-center text-gray-500 text-sm">No data</div>
      ) : (
        <div className="overflow-x-auto">
          <table className="min-w-full text-sm">
            <thead className="bg-gray-50 text-gray-500 text-xs uppercase">
              <tr>
                <th className="px-4 py-2 text-left">{data?.group_by}</th>
                <th className="px-4 py-2 text-right">{metric ? 'Samples' : 'Count'}</th>
                <th className="px-4 py-2 text-left w-1/5">Share</th>
                <th className="px-4 py-2 text-right">Sessions</th>
                {metric && <th className="px-4 py-2 text-right">p50</th>}
                {metric && <th className="px-4 py-2 text-right">p95</th>}
                {metric && <th className="px-4 py-2 text-right">p99</th>}
                {metric && <th className="px-4 py-2 text-right">Avg</th>}
                {metric && <th className="px-4 py-2 text-right">Max</th>}
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.value} className="border-t border-gray-100">
                  <td className="px-4 py-2 font-mono">
                    {r.value === '' ? <span className="text-gray-400">(not set)</span> : r.value}
                  </td>
                  <td className="px-4 py-2 text-right">{formatNumber(r.count)}</td>
                  <td className="px-4 py-2">
                    <ShareBar count={r.count} total={total} max={max} />
                  </td>
                  <td className="px-4 py-2 text-right">{formatNumber(r.sessions)}</td>
                  {metric && <td className="px-4 py-2 text-right">{formatMetric(r.p50, unit)}</td>}
                  {metric && <td className="px-4 py-2 text-right font-medium">{formatMetric(r.p95, unit)}</td>}
                  {metric && <td className="px-4 py-2 text-right">{formatMetric(r.p99, unit)}</td>}
                  {metric && <td className="px-4 py-2 text-right">{formatMetric(r.avg, unit)}</td>}
                  {metric && <td className="px-4 py-2 text-right">{formatMetric(r.max, unit)}</td>}
                  <td className="px-4 py-2 text-right">
                    {group.kind === 'key' && (
                      <button
                        onClick={() => addContextFilter({ key: group.key, value: r.value })}
                        className="text-xs text-blue-600 hover:underline"
                      >
                        Filter
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="px-4 py-2 text-xs text-gray-500 space-y-1">
            {data && data.merged > 0 && (
              <p>
                {formatNumber(data.merged)} {metric ? 'samples' : 'calls'} of {formatNumber(total)} are not in the table: their{' '}
                {group.kind === 'prop' ? 'properties were not kept (counted beyond the per-minute budget)' : 'value is unknown (merged across contexts on the device)'}.
              </p>
            )}
            {data?.truncated && <p>Showing the {rows.length} largest groups.</p>}
            {metric && <p>Percentiles from device-side histograms: exact rank, value within 2%.</p>}
          </div>
        </div>
      )}
    </div>
  )
}

function MetricsTab({ contextKeys }: { contextKeys: string[] }) {
  const { queryParams } = useFilters()
  const { data, isLoading, isError } = useCustomMetrics(queryParams)
  const [selected, setSelected] = useState('')
  const metrics = data?.metrics ?? []
  const name = selected || metrics[0]?.name || ''

  if (isLoading) return <div className="text-center py-8 text-gray-500">Loading...</div>
  if (isError) return <div className="text-center py-8 text-red-500">Could not load the metrics.</div>
  if (metrics.length === 0)
    return (
      <div className="bg-white rounded-lg shadow p-6 text-center text-gray-500 text-sm">
        No custom metrics in this range. The game records them with ApmClient.RecordMetric or
        StartTimer/StopTimer (SDK with APM-6).
      </div>
    )
  return (
    <>
      <div className="bg-white rounded-lg shadow overflow-x-auto">
        <table className="min-w-full text-sm">
          <thead className="bg-gray-50 text-gray-500 text-xs uppercase">
            <tr>
              <th className="px-4 py-2 text-left">Metric</th>
              <th className="px-4 py-2 text-right">Samples</th>
              <th className="px-4 py-2 text-right">Sessions</th>
              <th className="px-4 py-2 text-right">p50</th>
              <th className="px-4 py-2 text-right">p95</th>
              <th className="px-4 py-2 text-right">p99</th>
              <th className="px-4 py-2 text-right">Avg</th>
              <th className="px-4 py-2 text-right">Max</th>
            </tr>
          </thead>
          <tbody>
            {metrics.map((m) => (
              <tr
                key={m.name}
                onClick={() => setSelected(m.name || '')}
                className={`border-t border-gray-100 cursor-pointer hover:bg-gray-50 ${m.name === name ? 'bg-blue-50' : ''}`}
              >
                <td className="px-4 py-2 font-mono">{m.name}</td>
                <td className="px-4 py-2 text-right">{formatNumber(m.count)}</td>
                <td className="px-4 py-2 text-right">{formatNumber(m.sessions)}</td>
                <td className="px-4 py-2 text-right">{formatMetric(m.p50, m.unit || '')}</td>
                <td className="px-4 py-2 text-right font-medium">{formatMetric(m.p95, m.unit || '')}</td>
                <td className="px-4 py-2 text-right">{formatMetric(m.p99, m.unit || '')}</td>
                <td className="px-4 py-2 text-right">{formatMetric(m.avg, m.unit || '')}</td>
                <td className="px-4 py-2 text-right">{formatMetric(m.max, m.unit || '')}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {name && <Distribution key={name} kind="metrics" name={name} contextKeys={contextKeys} />}
    </>
  )
}

function EventsTab({ contextKeys }: { contextKeys: string[] }) {
  const { queryParams } = useFilters()
  const { data, isLoading, isError } = useCustomEvents(queryParams)
  const [selected, setSelected] = useState('')
  const events = data?.events ?? []
  const name = selected || events[0]?.name || ''

  if (isLoading) return <div className="text-center py-8 text-gray-500">Loading...</div>
  if (isError) return <div className="text-center py-8 text-red-500">Could not load the events.</div>
  if (events.length === 0)
    return (
      <div className="bg-white rounded-lg shadow p-6 text-center text-gray-500 text-sm">
        No custom events in this range. The game sends them with ApmClient.TrackEvent (SDK with APM-6).
      </div>
    )
  return (
    <>
      <div className="bg-white rounded-lg shadow overflow-x-auto">
        <table className="min-w-full text-sm">
          <thead className="bg-gray-50 text-gray-500 text-xs uppercase">
            <tr>
              <th className="px-4 py-2 text-left">Event</th>
              <th className="px-4 py-2 text-right">Count</th>
              <th className="px-4 py-2 text-right">Sessions</th>
              <th className="px-4 py-2 text-right">Devices</th>
              <th className="px-4 py-2 text-right">Last seen</th>
            </tr>
          </thead>
          <tbody>
            {events.map((e) => (
              <tr
                key={e.name}
                onClick={() => setSelected(e.name)}
                className={`border-t border-gray-100 cursor-pointer hover:bg-gray-50 ${e.name === name ? 'bg-blue-50' : ''}`}
              >
                <td className="px-4 py-2 font-mono">{e.name}</td>
                <td className="px-4 py-2 text-right">{formatNumber(e.count)}</td>
                <td className="px-4 py-2 text-right">{formatNumber(e.sessions)}</td>
                <td className="px-4 py-2 text-right">{formatNumber(e.devices)}</td>
                <td className="px-4 py-2 text-right text-gray-500">{formatDateTime(e.last_seen)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {name && <Distribution key={name} kind="events" name={name} contextKeys={contextKeys} />}
    </>
  )
}

// Custom metrics (RecordMetric, timers: p50/p95 per context value, version,
// device) and custom events (TrackEvent: counts) of APM-6.
export function Custom() {
  const { queryParams } = useFilters()
  const [tab, setTab] = useState<'metrics' | 'events'>('metrics')
  const { data: keyData } = useContextKeys({ ...queryParams, source: tab === 'metrics' ? 'custom_metrics' : 'custom_events' })
  const contextKeys = (keyData?.keys ?? []).map((k) => k.key)

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Custom Metrics &amp; Events</h1>
      <GlobalFilterBar />
      <div className="flex gap-2 mb-4" role="tablist">
        {(['metrics', 'events'] as const).map((t) => (
          <button
            key={t}
            role="tab"
            aria-selected={tab === t}
            onClick={() => setTab(t)}
            className={`px-4 py-1.5 text-sm rounded ${tab === t ? 'bg-slate-800 text-white' : 'bg-white text-gray-700 shadow'}`}
          >
            {t === 'metrics' ? 'Metrics' : 'Events'}
          </button>
        ))}
      </div>
      {tab === 'metrics' ? <MetricsTab contextKeys={contextKeys} /> : <EventsTab contextKeys={contextKeys} />}
    </div>
  )
}
