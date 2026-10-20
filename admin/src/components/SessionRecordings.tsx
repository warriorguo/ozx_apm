import { createContext, useContext } from 'react'
import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { getSessionFileAvailability, sessionFilesLink } from '../services/sessionFiles'
import type { SessionFileAvailability } from '../services/sessionFiles'

export function formatBytes(n: number): string {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + ' GB'
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB'
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(0) + ' KB'
  return n + ' B'
}

// Recordings of several sessions read with one request (a page of
// occurrences), for the SessionRecordings inside it.
const BatchContext = createContext<SessionFileAvailability[] | null>(null)

export function SessionRecordingsBatch({ sessionIds, children }: { sessionIds: string[]; children: ReactNode }) {
  const ids = Array.from(new Set(sessionIds.filter(Boolean))).sort()
  const { data } = useQuery({
    queryKey: ['session-file-availability', ids],
    queryFn: () => getSessionFileAvailability(ids),
    enabled: ids.length > 0,
    retry: false,
    staleTime: 60_000,
  })
  return <BatchContext.Provider value={data ?? []}>{children}</BatchContext.Provider>
}

// APM-13: the session's recordings (AiTelemetry session directories a
// development build uploaded), linked to the Session files page where they
// are downloaded with the token. Renders nothing when the session has none,
// when session files are not configured, or when the request fails.
export function SessionRecordings({ sessionId }: { sessionId: string }) {
  const batch = useContext(BatchContext)
  const { data } = useQuery({
    queryKey: ['session-file-availability', [sessionId]],
    queryFn: () => getSessionFileAvailability([sessionId]),
    enabled: !!sessionId && batch === null,
    retry: false,
    staleTime: 60_000,
  })
  const recordings = (batch ?? data ?? []).filter((r) => r.session_id === sessionId)
  if (!sessionId || recordings.length === 0) return null
  return (
    <span className="inline-flex flex-wrap gap-2" data-testid="session-recordings">
      {recordings.map((r) => (
        <Link
          key={r.upload_id}
          to={sessionFilesLink({ session_id: sessionId })}
          className="px-2 py-0.5 text-xs rounded bg-emerald-100 text-emerald-800 hover:bg-emerald-200"
          title="The AiTelemetry session directory this development build recorded (download needs the session files token)"
        >
          Recording ({formatBytes(r.size_bytes)})
        </Link>
      ))}
    </span>
  )
}
