import { useState } from 'react'
import type { TimePreset } from '../hooks/useFilters'
import { useFilters } from '../hooks/useFilters'
import {
  useAppVersions,
  useContextKeys,
  useContextValues,
  isValidContextKey,
} from '../hooks/useApi'
import type { BuildTypeFilter, ContextFilter, FilterParams } from '../types/api'

interface FilterBarProps {
  timePreset: TimePreset
  appVersion: string
  platform: string
  formattedRange: string
  onTimePresetChange: (preset: TimePreset) => void
  onAppVersionChange: (version: string) => void
  onPlatformChange: (platform: string) => void
  // APM-5. The build type selector is shown when onBuildTypeChange is set,
  // the context filters when onAddContextFilter is set.
  buildType?: BuildTypeFilter
  onBuildTypeChange?: (buildType: BuildTypeFilter) => void
  contextFilters?: ContextFilter[]
  onAddContextFilter?: (filter: ContextFilter) => void
  onRemoveContextFilter?: (index: number) => void
  // Range etc. for the key/value suggestions.
  suggestionParams?: FilterParams
}

const timePresets: { value: TimePreset; label: string }[] = [
  { value: '1h', label: '1 Hour' },
  { value: '6h', label: '6 Hours' },
  { value: '24h', label: '24 Hours' },
  { value: '7d', label: '7 Days' },
  { value: '30d', label: '30 Days' },
]

const platforms = ['', 'Android', 'iOS', 'Windows', 'macOS']

const buildTypes: { value: BuildTypeFilter; label: string }[] = [
  { value: 'release', label: 'Release' },
  { value: 'dev', label: 'Dev' },
  { value: 'editor', label: 'Editor' },
  { value: 'unknown', label: 'Unknown (old SDK)' },
  { value: 'all', label: 'All builds' },
]

export function FilterBar({
  timePreset,
  appVersion,
  platform,
  formattedRange,
  onTimePresetChange,
  onAppVersionChange,
  onPlatformChange,
  buildType = 'release',
  onBuildTypeChange,
  contextFilters = [],
  onAddContextFilter,
  onRemoveContextFilter,
  suggestionParams,
}: FilterBarProps) {
  const { data: versions = [] } = useAppVersions()

  return (
    <div className="bg-white rounded-lg shadow p-4 mb-6">
      <div className="flex flex-wrap items-center gap-4">
        <div className="flex items-center gap-2">
          <span className="text-sm text-gray-500">Time:</span>
          <div className="flex gap-1">
            {timePresets.map((preset) => (
              <button
                key={preset.value}
                onClick={() => onTimePresetChange(preset.value)}
                className={`px-3 py-1 text-sm rounded ${
                  timePreset === preset.value
                    ? 'bg-blue-500 text-white'
                    : 'bg-gray-100 text-gray-700 hover:bg-gray-200'
                }`}
              >
                {preset.label}
              </button>
            ))}
          </div>
        </div>

        <div className="flex items-center gap-2">
          <span className="text-sm text-gray-500">Version:</span>
          <select
            value={appVersion}
            onChange={(e) => onAppVersionChange(e.target.value)}
            className="px-3 py-1.5 text-sm border border-gray-300 rounded focus:outline-none focus:ring-2 focus:ring-blue-500"
          >
            <option value="">All Versions</option>
            {versions.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
        </div>

        <div className="flex items-center gap-2">
          <span className="text-sm text-gray-500">Platform:</span>
          <select
            value={platform}
            onChange={(e) => onPlatformChange(e.target.value)}
            className="px-3 py-1.5 text-sm border border-gray-300 rounded focus:outline-none focus:ring-2 focus:ring-blue-500"
          >
            <option value="">All Platforms</option>
            {platforms.filter(p => p).map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        </div>

        {onBuildTypeChange && (
          <div className="flex items-center gap-2">
            <span className="text-sm text-gray-500">Build:</span>
            <select
              aria-label="Build type"
              value={buildType}
              onChange={(e) => onBuildTypeChange(e.target.value as BuildTypeFilter)}
              className="px-3 py-1.5 text-sm border border-gray-300 rounded focus:outline-none focus:ring-2 focus:ring-blue-500"
              title="Release also includes events from SDKs that do not send a build type"
            >
              {buildTypes.map((b) => (
                <option key={b.value} value={b.value}>
                  {b.label}
                </option>
              ))}
            </select>
          </div>
        )}

        <div className="ml-auto text-sm text-gray-500">{formattedRange}</div>
      </div>

      {onAddContextFilter && (
        <ContextFilterEditor
          filters={contextFilters}
          onAdd={onAddContextFilter}
          onRemove={onRemoveContextFilter}
          suggestionParams={suggestionParams}
        />
      )}
    </div>
  )
}

interface ContextFilterEditorProps {
  filters: ContextFilter[]
  onAdd: (filter: ContextFilter) => void
  onRemove?: (index: number) => void
  suggestionParams?: FilterParams
}

const MAX_CONTEXT_FILTERS = 10

// Context filters as removable chips, plus a key/value input with
// suggestions from the data in the current range.
export function ContextFilterEditor({ filters, onAdd, onRemove, suggestionParams }: ContextFilterEditorProps) {
  const [key, setKey] = useState('')
  const [value, setValue] = useState('')
  const params = suggestionParams ?? {}
  const { data: keyData } = useContextKeys(params)
  const { data: valueData } = useContextValues(key, params)
  const keys = keyData?.keys ?? []
  const values = valueData?.values ?? []
  const keyValid = isValidContextKey(key)
  const canAdd = keyValid && value.length <= 256 && filters.length < MAX_CONTEXT_FILTERS

  const add = () => {
    if (!canAdd) return
    onAdd({ key, value })
    setValue('')
  }

  return (
    <div className="flex flex-wrap items-center gap-2 mt-3 pt-3 border-t border-gray-100">
      <span className="text-sm text-gray-500">Context:</span>
      {filters.map((f, i) => (
        <span
          key={`${f.key}:${f.value}`}
          className="inline-flex items-center gap-1 px-2 py-0.5 text-xs bg-blue-50 text-blue-800 rounded"
        >
          <span className="font-mono">
            {f.key} = {f.value === '' ? '(not set)' : f.value}
          </span>
          {onRemove && (
            <button
              aria-label={`Remove ${f.key} filter`}
              onClick={() => onRemove(i)}
              className="text-blue-500 hover:text-blue-800"
            >
              ×
            </button>
          )}
        </span>
      ))}
      <input
        aria-label="Context key"
        list="ozx-context-keys"
        placeholder="key"
        value={key}
        onChange={(e) => setKey(e.target.value.trim())}
        className={`w-36 px-2 py-1 text-sm border rounded font-mono ${
          key === '' || keyValid ? 'border-gray-300' : 'border-red-400'
        }`}
      />
      <datalist id="ozx-context-keys">
        {keys.map((k) => (
          <option key={k.key} value={k.key} />
        ))}
      </datalist>
      <input
        aria-label="Context value"
        list="ozx-context-values"
        placeholder="value (empty = not set)"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') add()
        }}
        className="w-44 px-2 py-1 text-sm border border-gray-300 rounded font-mono"
      />
      <datalist id="ozx-context-values">
        {values.map((v) => (
          <option key={v.value} value={v.value} />
        ))}
      </datalist>
      <button
        onClick={add}
        disabled={!canAdd}
        className="px-3 py-1 text-sm rounded bg-gray-100 text-gray-700 hover:bg-gray-200 disabled:opacity-50"
      >
        Add filter
      </button>
    </div>
  )
}

// GlobalFilterBar is the filter bar of every page, bound to the shared filters.
export function GlobalFilterBar() {
  const f = useFilters()
  // Suggestions use the range and build type, not the context filters
  // themselves (so other values of a filtered key are still offered).
  const suggestionParams: FilterParams = {
    start_time: f.queryParams.start_time,
    end_time: f.queryParams.end_time,
    build_type: f.queryParams.build_type,
  }
  return (
    <FilterBar
      timePreset={f.filters.timePreset}
      appVersion={f.filters.appVersion}
      platform={f.filters.platform}
      formattedRange={f.formattedRange}
      onTimePresetChange={f.setTimePreset}
      onAppVersionChange={f.setAppVersion}
      onPlatformChange={f.setPlatform}
      buildType={f.filters.buildType}
      onBuildTypeChange={f.setBuildType}
      contextFilters={f.filters.contextFilters}
      onAddContextFilter={f.addContextFilter}
      onRemoveContextFilter={f.removeContextFilter}
      suggestionParams={suggestionParams}
    />
  )
}
