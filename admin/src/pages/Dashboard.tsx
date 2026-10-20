import { GlobalFilterBar } from '../components/FilterBar'
import { StatCard } from '../components/StatCard'
import { TimeSeriesChart } from '../components/TimeSeriesChart'
import { useFilters } from '../hooks/useFilters'
import { useSummary, useTimeSeries } from '../hooks/useApi'
import { formatNumber, formatPercent, formatMs } from '../utils/format'

// APM-22: the crash rate is crashed sessions / sessions, a fraction (0.08 =
// 8%), counted as the Health page counts them (crash-free sessions = 1 -
// this). The server's crash_rate is crash reports per 1000 sessions and is
// not shown (it read 8000% for 2 crashes in 25 sessions).
// Above this fraction of crashed sessions the rate is shown in red.
export const CRASH_RATE_DANGER = 0.01

// The crashed-session rate as a percentage; "n/a" from a server before
// APM-22 (no crashed_session_rate).
export function crashRateText(rate: number | undefined): string {
  return rate === undefined || !Number.isFinite(rate) ? 'n/a' : formatPercent(rate)
}

export function crashRateIsHigh(rate: number | undefined): boolean {
  return rate !== undefined && rate > CRASH_RATE_DANGER
}

export function Dashboard() {
  const { queryParams } = useFilters()

  const { data: summary, isLoading: summaryLoading } = useSummary(queryParams)
  const { data: fpsTimeSeries } = useTimeSeries('fps', queryParams)
  const { data: crashTimeSeries } = useTimeSeries('crash_count', queryParams)
  const { data: jankTimeSeries } = useTimeSeries('jank_count', queryParams)

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Dashboard</h1>

      <GlobalFilterBar />

      {summaryLoading ? (
        <div className="text-center py-8 text-gray-500">Loading...</div>
      ) : summary ? (
        <>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
            <StatCard
              title="Total Sessions"
              value={formatNumber(summary.total_sessions)}
              color="default"
            />
            <StatCard
              title="Crash Rate (sessions)"
              value={crashRateText(summary.crashed_session_rate)}
              subtitle={
                summary.crashed_sessions === undefined
                  ? `${summary.crash_count} crash reports`
                  : `${formatNumber(summary.crashed_sessions)} of ${formatNumber(summary.total_sessions)} sessions crashed`
              }
              color={crashRateIsHigh(summary.crashed_session_rate) ? 'danger' : 'success'}
            />
            <StatCard
              title="Average FPS"
              value={summary.avg_fps.toFixed(1)}
              color={summary.avg_fps < 30 ? 'warning' : 'success'}
            />
            <StatCard
              title="Avg Startup Time"
              value={formatMs(summary.avg_startup_ms)}
              color={summary.avg_startup_ms > 5000 ? 'warning' : 'success'}
            />
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
            <StatCard
              title="Total Events"
              value={formatNumber(summary.total_events)}
              color="default"
            />
            <StatCard
              title="Exceptions"
              value={formatNumber(summary.exception_count)}
              color={summary.exception_count > 100 ? 'warning' : 'default'}
            />
            <StatCard
              title="Jank Events"
              value={formatNumber(summary.jank_count)}
              color={summary.jank_count > 50 ? 'warning' : 'default'}
            />
            <StatCard
              title="Crash Reports"
              value={formatNumber(summary.crash_count)}
              subtitle="Every type, also ANRs and force-stops"
              color={summary.crash_count > 0 ? 'danger' : 'success'}
            />
          </div>

          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-6">
            {fpsTimeSeries && (
              <TimeSeriesChart
                data={fpsTimeSeries.data}
                title="FPS Over Time"
                startTime={queryParams.start_time}
                endTime={queryParams.end_time}
                color="#10b981"
                formatValue={(v) => `${v.toFixed(1)} FPS`}
              />
            )}
            {crashTimeSeries && (
              <TimeSeriesChart
                data={crashTimeSeries.data}
                title="Crashes Over Time"
                startTime={queryParams.start_time}
                endTime={queryParams.end_time}
                color="#ef4444"
                formatValue={(v) => `${Math.round(v)} crashes`}
              />
            )}
          </div>

          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-6">
            {jankTimeSeries && (
              <TimeSeriesChart
                data={jankTimeSeries.data}
                title="Jank Events Over Time"
                startTime={queryParams.start_time}
                endTime={queryParams.end_time}
                color="#f59e0b"
                formatValue={(v) => `${Math.round(v)} janks`}
              />
            )}

            <div className="bg-white rounded-lg shadow p-6">
              <h3 className="text-sm font-medium text-gray-700 mb-4">Top Versions</h3>
              <div className="space-y-3">
                {summary.top_versions?.map((v) => (
                  <div key={v.version} className="flex items-center justify-between">
                    <span className="text-sm text-gray-900">{v.version}</span>
                    <div className="flex items-center gap-4 text-xs">
                      <span className="text-gray-500">
                        {formatNumber(v.session_count)} sessions
                      </span>
                      <span
                        data-testid={`version-crash-rate-${v.version}`}
                        className={`${
                          crashRateIsHigh(v.crashed_session_rate) ? 'text-red-500' : 'text-green-500'
                        }`}
                      >
                        {crashRateText(v.crashed_session_rate)} sessions crashed
                      </span>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          </div>

          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
            <div className="bg-white rounded-lg shadow p-6">
              <h3 className="text-sm font-medium text-gray-700 mb-4">Platform Distribution</h3>
              <div className="space-y-3">
                {summary.top_platforms?.map((p) => (
                  <div key={p.platform} className="flex items-center justify-between">
                    <span className="text-sm text-gray-900">{p.platform}</span>
                    <div className="flex items-center gap-4 text-xs">
                      <span className="text-gray-500">
                        {formatNumber(p.session_count)} sessions
                      </span>
                      <span className="text-blue-500">{p.avg_fps.toFixed(1)} avg FPS</span>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </>
      ) : (
        <div className="text-center py-8 text-gray-500">No data available</div>
      )}
    </div>
  )
}
