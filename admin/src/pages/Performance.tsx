import { FilterBar } from '../components/FilterBar'
import { TimeSeriesChart } from '../components/TimeSeriesChart'
import { DistributionChart } from '../components/DistributionChart'
import { useFilters } from '../hooks/useFilters'
import { useTimeSeries, useDistribution } from '../hooks/useApi'

export function Performance() {
  const {
    filters,
    queryParams,
    formattedRange,
    setTimePreset,
    setAppVersion,
    setPlatform,
  } = useFilters()

  const { data: fpsTimeSeries, isLoading: fpsTimeLoading } = useTimeSeries('fps', queryParams)
  const { data: frameTimeTimeSeries } = useTimeSeries('frame_time', queryParams)
  const { data: startupTimeSeries } = useTimeSeries('startup_time', queryParams)
  const { data: jankTimeSeries } = useTimeSeries('jank_count', queryParams)

  const { data: fpsDist, isLoading: fpsDistLoading } = useDistribution('fps', queryParams)
  const { data: frameTimeDist } = useDistribution('frame_time', queryParams)
  const { data: startupDist } = useDistribution('startup', queryParams)

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Performance Analysis</h1>

      <FilterBar
        timePreset={filters.timePreset}
        appVersion={filters.appVersion}
        platform={filters.platform}
        formattedRange={formattedRange}
        onTimePresetChange={setTimePreset}
        onAppVersionChange={setAppVersion}
        onPlatformChange={setPlatform}
      />

      <div className="space-y-6">
        <section>
          <h2 className="text-lg font-semibold text-gray-800 mb-4">FPS Metrics</h2>
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
            {fpsTimeLoading ? (
              <div className="bg-white rounded-lg shadow p-6 h-64 flex items-center justify-center text-gray-500">
                Loading...
              </div>
            ) : fpsTimeSeries ? (
              <TimeSeriesChart
                data={fpsTimeSeries.data}
                title="Average FPS Over Time"
                color="#10b981"
                formatValue={(v) => `${v.toFixed(1)} FPS`}
              />
            ) : null}

            {fpsDistLoading ? (
              <div className="bg-white rounded-lg shadow p-6 h-64 flex items-center justify-center text-gray-500">
                Loading...
              </div>
            ) : fpsDist ? (
              <DistributionChart data={fpsDist} title="FPS Distribution" color="#10b981" />
            ) : null}
          </div>
        </section>

        <section>
          <h2 className="text-lg font-semibold text-gray-800 mb-4">Frame Time</h2>
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
            {frameTimeTimeSeries && (
              <TimeSeriesChart
                data={frameTimeTimeSeries.data}
                title="Average Frame Time Over Time"
                color="#3b82f6"
                yAxisLabel="ms"
                formatValue={(v) => `${v.toFixed(1)}ms`}
              />
            )}
            {frameTimeDist && (
              <DistributionChart
                data={frameTimeDist}
                title="Frame Time Distribution (ms)"
                color="#3b82f6"
              />
            )}
          </div>
        </section>

        <section>
          <h2 className="text-lg font-semibold text-gray-800 mb-4">Startup Performance</h2>
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
            {startupTimeSeries && (
              <TimeSeriesChart
                data={startupTimeSeries.data}
                title="Average Startup Time Over Time"
                color="#8b5cf6"
                yAxisLabel="ms"
                formatValue={(v) => `${v.toFixed(0)}ms`}
              />
            )}
            {startupDist && (
              <DistributionChart
                data={startupDist}
                title="Startup Time Distribution (ms)"
                color="#8b5cf6"
              />
            )}
          </div>
        </section>

        <section>
          <h2 className="text-lg font-semibold text-gray-800 mb-4">Jank Analysis</h2>
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
            {jankTimeSeries && (
              <TimeSeriesChart
                data={jankTimeSeries.data}
                title="Jank Events Over Time"
                color="#f59e0b"
                formatValue={(v) => `${Math.round(v)} janks`}
              />
            )}
            <div className="bg-white rounded-lg shadow p-6">
              <h3 className="text-sm font-medium text-gray-700 mb-4">Jank Definition</h3>
              <ul className="text-sm text-gray-600 space-y-2">
                <li>- Single frame &gt; 50ms = jank event</li>
                <li>- Consecutive frames &gt; 33ms = sustained jank</li>
                <li>- Captures scene/level and last 10s GC info on jank</li>
              </ul>
            </div>
          </div>
        </section>
      </div>
    </div>
  )
}
