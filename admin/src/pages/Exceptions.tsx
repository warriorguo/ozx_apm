import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { GlobalFilterBar } from '../components/FilterBar'
import { DataTable } from '../components/DataTable'
import { Pagination } from '../components/Pagination'
import { useFilters } from '../hooks/useFilters'
import { useExceptions } from '../hooks/useApi'
import { formatNumber, formatDateTime } from '../utils/format'
import { exceptionDetailLink } from '../utils/timeline'
import type { ExceptionGroup, ExceptionSort } from '../types/api'

const sorts: { value: ExceptionSort; label: string }[] = [
  { value: 'count', label: 'Occurrences' },
  { value: 'sessions', label: 'Affected sessions' },
  { value: 'first_seen', label: 'First seen (newest)' },
  { value: 'last_seen', label: 'Last seen (newest)' },
]

export function Exceptions() {
  const { queryParams } = useFilters()
  const navigate = useNavigate()

  const [page, setPage] = useState(1)
  const [sort, setSort] = useState<ExceptionSort>('count')
  const [firstErrorOnly, setFirstErrorOnly] = useState(false)

  // A new filter or sort starts at the first page.
  const filterKey = JSON.stringify(queryParams)
  useEffect(() => setPage(1), [filterKey, sort, firstErrorOnly])

  const { data: exceptionsData, isLoading, isError } = useExceptions({
    ...queryParams,
    page,
    page_size: 20,
    sort,
    ...(firstErrorOnly ? { first_error: '1' } : {}),
  })

  const columns = [
    {
      key: 'message',
      header: 'Message',
      render: (item: ExceptionGroup) => (
        <div className="max-w-lg">
          {item.exception_type && (
            <p className="text-xs font-semibold text-gray-800 truncate" title={item.exception_type}>
              {item.exception_type}
            </p>
          )}
          <p className="font-mono text-xs truncate" title={item.message}>
            <Link
              to={exceptionDetailLink(item.fingerprint)}
              className="hover:underline"
              onClick={(e) => e.stopPropagation()}
            >
              {item.message || '(no message)'}
            </Link>
          </p>
          {item.throw_site && (
            <p className="font-mono text-xs text-gray-600 truncate mt-0.5" title={item.throw_site}>
              at {item.throw_site}
            </p>
          )}
          {item.call_site && item.call_site !== item.throw_site && (
            <p className="font-mono text-xs text-gray-500 truncate" title={item.call_site}>
              logged from {item.call_site}
            </p>
          )}
          <p className="text-xs text-gray-400 truncate mt-0.5">{item.fingerprint}</p>
        </div>
      ),
    },
    {
      key: 'count',
      header: 'Count',
      render: (item: ExceptionGroup) => (
        <span className="font-medium text-amber-600">{formatNumber(item.count)}</span>
      ),
    },
    {
      key: 'session_count',
      header: 'Sessions',
      render: (item: ExceptionGroup) => formatNumber(item.session_count),
    },
    {
      key: 'first_error_sessions',
      header: 'First error in',
      render: (item: ExceptionGroup) =>
        item.first_error_sessions ? (
          <span title="Sessions in which this was the first error (a likely root cause)">
            {formatNumber(item.first_error_sessions)} sessions
          </span>
        ) : (
          <span className="text-gray-400">-</span>
        ),
    },
    {
      key: 'first_seen',
      header: 'First Seen',
      render: (item: ExceptionGroup) => (
        <span className="text-gray-500 text-xs">{formatDateTime(item.first_seen)}</span>
      ),
    },
    {
      key: 'last_seen',
      header: 'Last Seen',
      render: (item: ExceptionGroup) => (
        <span className="text-gray-500 text-xs">{formatDateTime(item.last_seen)}</span>
      ),
    },
  ]

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Exceptions</h1>

      <GlobalFilterBar />

      <div className="flex flex-wrap items-center gap-4 mb-4 text-sm">
        <label className="flex items-center gap-2">
          <span className="text-gray-500">Sort by:</span>
          <select
            aria-label="Sort by"
            value={sort}
            onChange={(e) => setSort(e.target.value as ExceptionSort)}
            className="px-2 py-1 border border-gray-300 rounded"
          >
            {sorts.map((s) => (
              <option key={s.value} value={s.value}>
                {s.label}
              </option>
            ))}
          </select>
        </label>
        <label className="flex items-center gap-2" title="Only occurrences that were the first error of their session (SDK with APM-3): root causes rather than follow-up errors">
          <input
            type="checkbox"
            checked={firstErrorOnly}
            onChange={(e) => setFirstErrorOnly(e.target.checked)}
          />
          <span className="text-gray-700">First error of the session only</span>
        </label>
      </div>

      {isError ? (
        <div className="bg-white rounded-lg shadow p-8 text-center text-red-600">Could not load the exceptions.</div>
      ) : (
        <DataTable
          data={exceptionsData?.exceptions || []}
          columns={columns}
          isLoading={isLoading}
          emptyMessage={firstErrorOnly ? 'No exception was the first error of a session in this range' : 'No exceptions found'}
          onRowClick={(item) => navigate(exceptionDetailLink(item.fingerprint))}
        />
      )}

      {exceptionsData && (
        <Pagination
          page={exceptionsData.page}
          pageSize={exceptionsData.page_size}
          totalCount={exceptionsData.total_count}
          onPageChange={setPage}
        />
      )}

      <div className="mt-6 bg-blue-50 border border-blue-200 rounded-lg p-4">
        <h3 className="text-sm font-medium text-blue-800 mb-2">About Exceptions</h3>
        <p className="text-sm text-blue-700">
          Exceptions are non-fatal errors caught by the SDK. They are grouped by a fingerprint
          the server computes from the exception type and where it was thrown (for an
          exception logged inside an error, such as an event bus handler&apos;s, the embedded
          exception counts, not the logging call); errors without a stack are grouped by
          their message with ids and numbers removed. Counts are occurrences. Click a group
          for its stack, occurrences, breadcrumbs and distribution.
        </p>
      </div>
    </div>
  )
}
