import { useEffect, useState } from 'react'
import { Navigate, useNavigate, useSearchParams } from 'react-router-dom'
import { GlobalFilterBar } from '../components/FilterBar'
import { IssueStatusBadge } from '../components/IssueStatus'
import { DataTable } from '../components/DataTable'
import { Pagination } from '../components/Pagination'
import { useFilters } from '../hooks/useFilters'
import { useCrashes } from '../hooks/useApi'
import { formatNumber, formatDateTime } from '../utils/format'
import { crashDetailLink, crashTypeLabels } from './CrashDetail'
import type { CrashGroup } from '../types/api'

// The crash groups (APM-7): native crashes, ANRs, Java crashes and kills of
// the game in the foreground, reported by the SDK at the next launch. A row
// opens the group's detail page.
export function Crashes() {
  const { queryParams } = useFilters()
  const navigate = useNavigate()
  const [search] = useSearchParams()
  // ?fingerprint=F (the link in an alert, APM-8) opens that group's detail page.
  const linked = search.get('fingerprint')
  const [page, setPage] = useState(1)
  // Another filter starts at the first page again.
  const filterKey = JSON.stringify(queryParams)
  useEffect(() => setPage(1), [filterKey])

  const { data: crashesData, isLoading } = useCrashes({
    ...queryParams,
    page,
    page_size: 20,
  })

  const columns = [
    {
      key: 'crash_type',
      header: 'Type',
      render: (item: CrashGroup) => (
        <span
          className="font-mono text-xs bg-red-100 text-red-800 px-2 py-0.5 rounded whitespace-nowrap"
          title={crashTypeLabels[item.crash_type] ?? item.crash_type}
        >
          {item.crash_type}
        </span>
      ),
    },
    {
      key: 'sample_message',
      header: 'Crash',
      render: (item: CrashGroup) => (
        <div className="max-w-xl">
          <div className="truncate text-sm font-mono" title={item.sample_message}>
            {item.sample_message || crashTypeLabels[item.crash_type] || item.crash_type}
          </div>
          {item.top_frame && (
            <div className={`truncate text-xs font-mono ${item.symbolicated ? 'text-blue-700' : 'text-gray-500'}`} title={item.top_frame}>
              {item.top_frame}
            </div>
          )}
        </div>
      ),
    },
    {
      key: 'status',
      header: 'Status',
      render: (item: CrashGroup) => <IssueStatusBadge info={item} />,
    },
    {
      key: 'count',
      header: 'Count',
      render: (item: CrashGroup) => <span className="font-medium">{formatNumber(item.count)}</span>,
    },
    {
      key: 'session_count',
      header: 'Sessions',
      render: (item: CrashGroup) => formatNumber(item.session_count),
    },
    {
      key: 'foreground',
      header: 'Foreground',
      render: (item: CrashGroup) => (item.foreground === undefined ? '-' : formatNumber(item.foreground)),
    },
    {
      key: 'last_seen',
      header: 'Last Seen',
      render: (item: CrashGroup) => <span className="text-gray-500 text-xs">{formatDateTime(item.last_seen)}</span>,
    },
    {
      key: 'affected_versions',
      header: 'Versions',
      render: (item: CrashGroup) => (
        <div className="flex flex-wrap gap-1">
          {item.affected_versions?.slice(0, 3).map((v) => (
            <span key={v} className="text-xs bg-gray-100 text-gray-700 px-1.5 py-0.5 rounded">
              {v}
            </span>
          ))}
          {item.affected_versions?.length > 3 && (
            <span className="text-xs text-gray-400">+{item.affected_versions.length - 3}</span>
          )}
        </div>
      ),
    },
  ]

  if (linked) {
    return <Navigate to={crashDetailLink(linked)} replace />
  }

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Crashes</h1>

      <GlobalFilterBar />

      <DataTable
        data={crashesData?.crashes || []}
        columns={columns}
        isLoading={isLoading}
        emptyMessage="No crashes found"
        onRowClick={(item) => navigate(crashDetailLink(item.fingerprint))}
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
  )
}
