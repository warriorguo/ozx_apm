import { useState } from 'react'
import { useAppVersions, useSetIssueState } from '../hooks/useApi'
import { formatDateTime } from '../utils/format'
import type { IssueKind, IssueStatusInfo } from '../types/api'

// APM-8: an exception or crash group's state. Every value (versions,
// fingerprints) is game data and is only rendered as text.

export function IssueStatusBadge({ info }: { info: IssueStatusInfo }) {
  if (!info.status) {
    return null
  }
  if (info.status === 'resolved') {
    return (
      <span
        className="text-xs bg-green-100 text-green-800 px-1.5 py-0.5 rounded whitespace-nowrap"
        title={info.resolved_in_version ? `Fixed in ${info.resolved_in_version}` : 'Resolved'}
      >
        Resolved{info.resolved_in_version ? ` in ${info.resolved_in_version}` : ''}
      </span>
    )
  }
  if (info.regressed_in_version) {
    return (
      <span
        className="text-xs bg-red-100 text-red-800 px-1.5 py-0.5 rounded whitespace-nowrap"
        title="It was resolved and occurred again in a newer version"
      >
        Regressed in {info.regressed_in_version}
      </span>
    )
  }
  return <span className="text-xs bg-gray-100 text-gray-600 px-1.5 py-0.5 rounded whitespace-nowrap">Unresolved</span>
}

function errorText(err: unknown): string {
  const e = err as { response?: { status?: number; data?: unknown } }
  const body = typeof e?.response?.data === 'string' ? e.response.data.trim() : ''
  if (e?.response?.status === 404) {
    return 'No occurrence of this group in the retained data (30 days).'
  }
  return body ? `Could not change the state: ${body}` : 'Could not change the state.'
}

export function IssueStatusControl({
  kind,
  fingerprint,
  info,
}: {
  kind: IssueKind
  fingerprint: string
  info: IssueStatusInfo
}) {
  const [version, setVersion] = useState('')
  const [comment, setComment] = useState('')
  const mutation = useSetIssueState()
  const { data: versions } = useAppVersions()

  if (!info.status) {
    return null // an older server, or the states could not be read
  }
  const listId = `issue-versions-${kind}`

  return (
    <div className="border border-gray-200 rounded p-3 text-sm" data-testid="issue-status">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-gray-500">Status:</span>
        <IssueStatusBadge info={info} />
        {info.status === 'resolved' && info.resolved_at && (
          <span className="text-xs text-gray-500">since {formatDateTime(info.resolved_at)}</span>
        )}
      </div>
      {info.status === 'resolved' ? (
        <div className="mt-2">
          <p className="text-xs text-gray-500">
            An occurrence after the resolution in{' '}
            {info.resolved_in_version ? `${info.resolved_in_version} or a newer version` : 'a newer version than the last one that had it'}{' '}
            reopens it and sends a regression alert.
          </p>
          <button
            type="button"
            className="mt-2 px-3 py-1 text-xs border border-gray-300 rounded hover:bg-gray-50 disabled:opacity-50"
            disabled={mutation.isPending}
            onClick={() => mutation.mutate({ kind, fingerprint, status: 'unresolved' })}
          >
            Reopen
          </button>
        </div>
      ) : (
        <form
          className="mt-2 flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            mutation.mutate({
              kind,
              fingerprint,
              status: 'resolved',
              resolved_in_version: version.trim() || undefined,
              comment: comment.trim() || undefined,
            })
          }}
        >
          <label className="flex flex-col text-xs text-gray-500">
            Fixed in version (optional)
            <input
              aria-label="Fixed in version"
              list={listId}
              value={version}
              maxLength={128}
              onChange={(e) => setVersion(e.target.value)}
              placeholder="next version"
              className="mt-1 px-2 py-1 border border-gray-300 rounded text-sm text-gray-900"
            />
            <datalist id={listId}>
              {(versions || []).map((v) => (
                <option key={v} value={v} />
              ))}
            </datalist>
          </label>
          <label className="flex flex-col text-xs text-gray-500">
            Comment (optional)
            <input
              aria-label="Comment"
              value={comment}
              maxLength={500}
              onChange={(e) => setComment(e.target.value)}
              className="mt-1 px-2 py-1 border border-gray-300 rounded text-sm text-gray-900"
            />
          </label>
          <button
            type="submit"
            className="px-3 py-1 text-xs bg-green-600 text-white rounded hover:bg-green-700 disabled:opacity-50"
            disabled={mutation.isPending}
          >
            Mark resolved
          </button>
          <p className="w-full text-xs text-gray-500">
            Empty: any version newer than the newest one that has it counts as a regression.
          </p>
        </form>
      )}
      {mutation.isError && (
        <p className="mt-2 text-xs text-red-600" role="alert">
          {errorText(mutation.error)}
        </p>
      )}
    </div>
  )
}
