import { useState } from 'react'
import { GlobalFilterBar } from '../components/FilterBar'
import { DataTable } from '../components/DataTable'
import { Pagination } from '../components/Pagination'
import { useFilters } from '../hooks/useFilters'
import { useExceptions } from '../hooks/useApi'
import { formatNumber, formatDateTime } from '../utils/format'
import type { ExceptionGroup } from '../types/api'

export function Exceptions() {
  const { queryParams } = useFilters()

  const [page, setPage] = useState(1)

  const { data: exceptionsData, isLoading } = useExceptions({
    ...queryParams,
    page,
    page_size: 20,
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
            {item.message}
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

      <DataTable
        data={exceptionsData?.exceptions || []}
        columns={columns}
        isLoading={isLoading}
        emptyMessage="No exceptions found"
      />

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
          their message with ids and numbers removed. High exception counts may indicate
          issues that need attention even if they don&apos;t cause crashes.
        </p>
      </div>
    </div>
  )
}
