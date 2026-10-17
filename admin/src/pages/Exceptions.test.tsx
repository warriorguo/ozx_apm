import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { FiltersProvider } from '../hooks/useFilters'
import { Exceptions } from './Exceptions'

const { api } = vi.hoisted(() => ({
  api: {
    getAppVersions: vi.fn(),
    getExceptions: vi.fn(),
  },
}))

vi.mock('../services/api', () => api)

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return (
    <QueryClientProvider client={client}>
      <FiltersProvider>{children}</FiltersProvider>
    </QueryClientProvider>
  )
}

describe('Exceptions page (APM-4)', () => {
  beforeEach(() => {
    api.getAppVersions.mockResolvedValue([])
  })

  it('shows the real type and throw site of a group, and old groups as before', async () => {
    api.getExceptions.mockResolvedValue({
      exceptions: [
        {
          fingerprint: 'fad75585e1523085',
          message: '[GameEventBus] Error handling event RoomEnteredEvent: System.InvalidOperationException: Room 12',
          count: 5,
          session_count: 2,
          first_seen: '2026-10-17T10:00:00Z',
          last_seen: '2026-10-17T11:00:00Z',
          fingerprint_version: 1,
          exception_type: 'System.InvalidOperationException',
          message_template: 'Room <n>',
          throw_site: 'Ozx.Rooms.RoomLoader.LoadTemplate (at Assets/Scripts/Rooms/RoomLoader.cs:71)',
          call_site: 'Ozx.Core.GameEventBus.Publish (at Assets/Scripts/Core/GameEventBus.cs:52)',
        },
        {
          // stored before APM-4: no type, sites or version
          fingerprint: 'a1b2c3d4e5f60718',
          message: 'NullReferenceException: Object reference not set to an instance of an object',
          count: 3,
          session_count: 1,
          first_seen: '2026-10-16T10:00:00Z',
          last_seen: '2026-10-16T11:00:00Z',
        },
      ],
      total_count: 2,
      page: 1,
      page_size: 20,
    })
    render(<Exceptions />, { wrapper })
    expect(await screen.findByText('System.InvalidOperationException')).toBeInTheDocument()
    expect(screen.getByText('at Ozx.Rooms.RoomLoader.LoadTemplate (at Assets/Scripts/Rooms/RoomLoader.cs:71)')).toBeInTheDocument()
    expect(screen.getByText('logged from Ozx.Core.GameEventBus.Publish (at Assets/Scripts/Core/GameEventBus.cs:52)')).toBeInTheDocument()
    expect(screen.getByText('NullReferenceException: Object reference not set to an instance of an object')).toBeInTheDocument()
    expect(screen.getByText('a1b2c3d4e5f60718')).toBeInTheDocument()
    expect(screen.getAllByText(/^at /)).toHaveLength(1)
  })
})
