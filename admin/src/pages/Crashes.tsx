import { useState } from 'react'
import { FilterBar } from '../components/FilterBar'
import { DataTable } from '../components/DataTable'
import { Pagination } from '../components/Pagination'
import { useFilters } from '../hooks/useFilters'
import { useCrashes, useCrashDetail } from '../hooks/useApi'
import { formatNumber, formatDateTime } from '../utils/format'
import type { CrashGroup } from '../types/api'

export function Crashes() {
  const {
    filters,
    queryParams,
    formattedRange,
    setTimePreset,
    setAppVersion,
    setPlatform,
  } = useFilters()

  const [page, setPage] = useState(1)
  const [selectedFingerprint, setSelectedFingerprint] = useState<string | null>(null)

  const { data: crashesData, isLoading } = useCrashes({
    ...queryParams,
    page,
    page_size: 20,
  })

  const { data: crashDetail } = useCrashDetail(selectedFingerprint || '', queryParams)

  const columns = [
    {
      key: 'crash_type',
      header: 'Type',
      render: (item: CrashGroup) => (
        <span className="font-mono text-xs bg-red-100 text-red-800 px-2 py-0.5 rounded">
          {item.crash_type}
        </span>
      ),
    },
    {
      key: 'sample_message',
      header: 'Message',
      render: (item: CrashGroup) => (
        <div className="max-w-md truncate text-sm" title={item.sample_message}>
          {item.sample_message}
        </div>
      ),
    },
    {
      key: 'count',
      header: 'Count',
      render: (item: CrashGroup) => (
        <span className="font-medium">{formatNumber(item.count)}</span>
      ),
    },
    {
      key: 'session_count',
      header: 'Sessions',
      render: (item: CrashGroup) => formatNumber(item.session_count),
    },
    {
      key: 'last_seen',
      header: 'Last Seen',
      render: (item: CrashGroup) => (
        <span className="text-gray-500 text-xs">{formatDateTime(item.last_seen)}</span>
      ),
    },
    {
      key: 'affected_versions',
      header: 'Versions',
      render: (item: CrashGroup) => (
        <div className="flex flex-wrap gap-1">
          {item.affected_versions?.slice(0, 3).map((v) => (
            <span
              key={v}
              className="text-xs bg-gray-100 text-gray-700 px-1.5 py-0.5 rounded"
            >
              {v}
            </span>
          ))}
          {item.affected_versions?.length > 3 && (
            <span className="text-xs text-gray-400">
              +{item.affected_versions.length - 3}
            </span>
          )}
        </div>
      ),
    },
  ]

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Crashes</h1>

      <FilterBar
        timePreset={filters.timePreset}
        appVersion={filters.appVersion}
        platform={filters.platform}
        formattedRange={formattedRange}
        onTimePresetChange={setTimePreset}
        onAppVersionChange={setAppVersion}
        onPlatformChange={setPlatform}
      />

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <div className="lg:col-span-2">
          <DataTable
            data={crashesData?.crashes || []}
            columns={columns}
            isLoading={isLoading}
            emptyMessage="No crashes found"
            onRowClick={(item) => setSelectedFingerprint(item.fingerprint)}
          />
          {crashesData && (
            <Pagination
              page={crashesData.page}
              pageSize={crashesData.page_size}
              totalCount={crashesData.total_count}
              onPageChange={setPage}
            />
          )}
        </div>

        <div>
          {selectedFingerprint && crashDetail ? (
            <div className="bg-white rounded-lg shadow p-6 sticky top-6">
              <h3 className="text-sm font-medium text-gray-700 mb-4">Crash Detail</h3>

              <div className="space-y-4">
                <div>
                  <p className="text-xs text-gray-500 mb-1">Type</p>
                  <span className="font-mono text-xs bg-red-100 text-red-800 px-2 py-0.5 rounded">
                    {crashDetail.crash_type}
                  </span>
                </div>

                <div>
                  <p className="text-xs text-gray-500 mb-1">Count</p>
                  <p className="text-sm font-medium">
                    {formatNumber(crashDetail.count)} crashes in{' '}
                    {formatNumber(crashDetail.session_count)} sessions
                  </p>
                </div>

                <div>
                  <p className="text-xs text-gray-500 mb-1">Time Range</p>
                  <p className="text-xs text-gray-700">
                    {formatDateTime(crashDetail.first_seen)} -{' '}
                    {formatDateTime(crashDetail.last_seen)}
                  </p>
                </div>

                <div>
                  <p className="text-xs text-gray-500 mb-1">Stack Trace</p>
                  <pre className="text-xs bg-gray-50 p-3 rounded overflow-auto max-h-48 font-mono">
                    {crashDetail.stack}
                  </pre>
                </div>

                <div>
                  <p className="text-xs text-gray-500 mb-1">Version Distribution</p>
                  <div className="space-y-1">
                    {crashDetail.version_distribution?.slice(0, 5).map((v) => (
                      <div key={v.version} className="flex justify-between text-xs">
                        <span>{v.version}</span>
                        <span className="text-gray-500">{formatNumber(v.count)}</span>
                      </div>
                    ))}
                  </div>
                </div>

                <div>
                  <p className="text-xs text-gray-500 mb-1">Device Distribution</p>
                  <div className="space-y-1">
                    {crashDetail.device_distribution?.slice(0, 5).map((d) => (
                      <div key={d.device} className="flex justify-between text-xs">
                        <span className="truncate max-w-32">{d.device}</span>
                        <span className="text-gray-500">{formatNumber(d.count)}</span>
                      </div>
                    ))}
                  </div>
                </div>

                {crashDetail.occurrences && crashDetail.occurrences.length > 0 && (
                  <div>
                    <p className="text-xs text-gray-500 mb-1">Recent Occurrences</p>
                    <div className="space-y-2">
                      {crashDetail.occurrences.slice(0, 3).map((occ, i) => (
                        <div key={i} className="text-xs bg-gray-50 p-2 rounded">
                          <p>
                            <span className="text-gray-500">Time:</span>{' '}
                            {formatDateTime(occ.timestamp)}
                          </p>
                          <p>
                            <span className="text-gray-500">Device:</span> {occ.device_model}
                          </p>
                          <p>
                            <span className="text-gray-500">Scene:</span> {occ.scene}
                          </p>
                          {occ.breadcrumbs && occ.breadcrumbs.length > 0 && (
                            <details className="mt-1">
                              <summary className="text-gray-500 cursor-pointer">
                                Breadcrumbs ({occ.breadcrumbs.length})
                              </summary>
                              <ul className="pl-2 mt-1 text-gray-600">
                                {occ.breadcrumbs.slice(-5).map((b, j) => (
                                  <li key={j}>- {b}</li>
                                ))}
                              </ul>
                            </details>
                          )}
                        </div>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            </div>
          ) : (
            <div className="bg-white rounded-lg shadow p-6 text-center text-gray-500 text-sm">
              Select a crash to view details
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
