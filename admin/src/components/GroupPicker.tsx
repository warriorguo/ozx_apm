import { useContextKeys } from '../hooks/useApi'
import type { AnalysisColumn, AnalysisGroup, FilterParams } from '../types/api'

const columnLabels: Record<AnalysisColumn, string> = {
  app_version: 'Version',
  build_type: 'Build type',
  device_model: 'Device',
  platform: 'Platform',
  os_version: 'OS',
  ram_class: 'RAM class',
}

// Game text (versions, context values, names) shown in tables: always as
// text, cut to max characters with the full text in the title.
export function Clipped({ text, max = 120, empty = '(not set)' }: { text: string; max?: number; empty?: string }) {
  if (text === '') return <span className="text-gray-400">{empty}</span>
  const s = text.length > max ? text.slice(0, max) + '…' : text
  return <span title={text.length > max ? text : undefined}>{s}</span>
}

// Encodes a group as the select's value: "by:app_version" or "key:stageType".
function encode(g: AnalysisGroup): string {
  return `${g.kind}:${g.key}`
}

// A select of columns and the game context keys of the range (APM-5).
export function GroupPicker({
  value,
  onChange,
  columns,
  params,
  label = 'Group by',
}: {
  value: AnalysisGroup
  onChange: (g: AnalysisGroup) => void
  columns: AnalysisColumn[]
  params: FilterParams
  label?: string
}) {
  const { data } = useContextKeys(params)
  const keys = data?.keys ?? []
  const current = encode(value)
  const hasCurrent = value.kind === 'by' || keys.some((k) => k.key === value.key)
  return (
    <label className="flex items-center gap-2 text-sm text-gray-500">
      {label}:
      <select
        aria-label={label}
        value={current}
        onChange={(e) => {
          const v = e.target.value
          const i = v.indexOf(':')
          const kind = v.slice(0, i)
          const key = v.slice(i + 1)
          onChange(kind === 'by' ? { kind: 'by', key: key as AnalysisColumn } : { kind: 'key', key })
        }}
        className="px-3 py-1.5 text-sm border border-gray-300 rounded text-gray-900"
      >
        <optgroup label="Columns">
          {columns.map((c) => (
            <option key={c} value={`by:${c}`}>
              {columnLabels[c]}
            </option>
          ))}
        </optgroup>
        <optgroup label="Game context">
          {!hasCurrent && <option value={current}>{value.key}</option>}
          {keys.map((k) => (
            <option key={k.key} value={`key:${k.key}`}>
              {k.key}
            </option>
          ))}
        </optgroup>
      </select>
    </label>
  )
}

export function groupLabel(g: AnalysisGroup): string {
  return g.kind === 'by' ? columnLabels[g.key] : g.key
}
