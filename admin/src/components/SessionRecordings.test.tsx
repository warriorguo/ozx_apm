import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import type { ReactNode } from 'react'
import { SessionRecordings, SessionRecordingsBatch, formatBytes } from './SessionRecordings'

const sf = vi.hoisted(() => ({
  getSessionFileAvailability: vi.fn(),
  sessionFilesLink: (p: Record<string, string>) => '/session-files?' + new URLSearchParams(p).toString(),
}))
vi.mock('../services/sessionFiles', () => sf)

function wrap(children: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('SessionRecordings (APM-13)', () => {
  beforeEach(() => sf.getSessionFileAvailability.mockReset())

  it('links a session with a recording to the session files page', async () => {
    sf.getSessionFileAvailability.mockResolvedValue([{ session_id: 's1', upload_id: 'u1', size_bytes: 3 << 20, created_at: '' }])
    wrap(<SessionRecordings sessionId="s1" />)
    const link = await screen.findByText('Recording (3.0 MB)')
    expect(link.closest('a')?.getAttribute('href')).toBe('/session-files?session_id=s1')
    expect(sf.getSessionFileAvailability).toHaveBeenCalledWith(['s1'])
  })

  it('shows nothing for a session without a recording', async () => {
    sf.getSessionFileAvailability.mockResolvedValue([{ session_id: 'other', upload_id: 'u', size_bytes: 1, created_at: '' }])
    wrap(<SessionRecordings sessionId="s1" />)
    await waitFor(() => expect(sf.getSessionFileAvailability).toHaveBeenCalled())
    expect(screen.queryByTestId('session-recordings')).toBeNull()
  })

  it('a page of occurrences asks once for all its sessions', async () => {
    sf.getSessionFileAvailability.mockResolvedValue([{ session_id: 's2', upload_id: 'u2', size_bytes: 2048, created_at: '' }])
    wrap(
      <SessionRecordingsBatch sessionIds={['s2', 's1', 's2', '']}>
        <SessionRecordings sessionId="s1" />
        <SessionRecordings sessionId="s2" />
      </SessionRecordingsBatch>,
    )
    await screen.findByText('Recording (2 KB)')
    expect(sf.getSessionFileAvailability).toHaveBeenCalledTimes(1)
    expect(sf.getSessionFileAvailability).toHaveBeenCalledWith(['s1', 's2'])
    expect(screen.getAllByTestId('session-recordings')).toHaveLength(1)
  })

  it('formats sizes', () => {
    expect(formatBytes(500)).toBe('500 B')
    expect(formatBytes(50 * 1024 * 1024)).toBe('50.0 MB')
  })
})
