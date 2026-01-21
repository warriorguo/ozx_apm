import type { TimePreset } from '../hooks/useFilters'
import { useAppVersions } from '../hooks/useApi'

interface FilterBarProps {
  timePreset: TimePreset
  appVersion: string
  platform: string
  formattedRange: string
  onTimePresetChange: (preset: TimePreset) => void
  onAppVersionChange: (version: string) => void
  onPlatformChange: (platform: string) => void
}

const timePresets: { value: TimePreset; label: string }[] = [
  { value: '1h', label: '1 Hour' },
  { value: '6h', label: '6 Hours' },
  { value: '24h', label: '24 Hours' },
  { value: '7d', label: '7 Days' },
  { value: '30d', label: '30 Days' },
]

const platforms = ['', 'Android', 'iOS', 'Windows', 'macOS']

export function FilterBar({
  timePreset,
  appVersion,
  platform,
  formattedRange,
  onTimePresetChange,
  onAppVersionChange,
  onPlatformChange,
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

        <div className="ml-auto text-sm text-gray-500">{formattedRange}</div>
      </div>
    </div>
  )
}
