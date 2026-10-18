import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import type { ReactNode } from 'react'
import { FiltersProvider } from '../hooks/useFilters'
import { Exceptions } from './Exceptions'

const { api } = vi.hoisted(() => ({
  api: {
    getAppVersions: vi.fn(),
    getExceptions: vi.fn(),
    getContextKeys: vi.fn(),
    getContextValues: vi.fn(),
  },
}))

vi.mock('../services/api', () => api)

function Where() {
  const loc = useLocation()
  return <p data-testid="where">{loc.pathname + loc.search}</p>
}

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return (
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/exceptions']}>
        <FiltersProvider>
          <Routes>
            <Route path="/exceptions" element={children} />
            <Route path="/exceptions/detail" element={<Where />} />
          </Routes>
        </FiltersProvider>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

const groups = {
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
      first_error_sessions: 1,
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
}

describe('Exceptions page (APM-4)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAppVersions.mockResolvedValue([])
    api.getContextKeys.mockResolvedValue({ keys: [] })
    api.getContextValues.mockResolvedValue({ key: '', values: [] })
  })

  it('shows the real type and throw site of a group, and old groups as before', async () => {
    api.getExceptions.mockResolvedValue(groups)
    render(<Exceptions />, { wrapper })
    expect(await screen.findByText('System.InvalidOperationException')).toBeInTheDocument()
    expect(screen.getByText('at Ozx.Rooms.RoomLoader.LoadTemplate (at Assets/Scripts/Rooms/RoomLoader.cs:71)')).toBeInTheDocument()
    expect(screen.getByText('logged from Ozx.Core.GameEventBus.Publish (at Assets/Scripts/Core/GameEventBus.cs:52)')).toBeInTheDocument()
    expect(screen.getByText('NullReferenceException: Object reference not set to an instance of an object')).toBeInTheDocument()
    expect(screen.getByText('a1b2c3d4e5f60718')).toBeInTheDocument()
    expect(screen.getAllByText(/^at /)).toHaveLength(1)
  })
})

describe('Exceptions page (APM-11)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAppVersions.mockResolvedValue([])
    api.getContextKeys.mockResolvedValue({ keys: [] })
    api.getContextValues.mockResolvedValue({ key: '', values: [] })
  })

  it('asks for release builds sorted by count by default, then by the chosen sort and first errors only', async () => {
    api.getExceptions.mockResolvedValue(groups)
    render(<Exceptions />, { wrapper })
    await screen.findByText('System.InvalidOperationException')
    expect(api.getExceptions).toHaveBeenLastCalledWith(expect.objectContaining({ build_type: 'release', sort: 'count', page: 1 }))
    expect(api.getExceptions.mock.lastCall?.[0].first_error).toBeUndefined()
    expect(screen.getByText('1 sessions')).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('Sort by'), { target: { value: 'first_seen' } })
    await waitFor(() => expect(api.getExceptions).toHaveBeenLastCalledWith(expect.objectContaining({ sort: 'first_seen' })))

    fireEvent.click(screen.getByLabelText('First error of the session only'))
    await waitFor(() =>
      expect(api.getExceptions).toHaveBeenLastCalledWith(expect.objectContaining({ sort: 'first_seen', first_error: '1' }))
    )
  })

  it('opens the detail page of a clicked group, with the fingerprint encoded', async () => {
    api.getExceptions.mockResolvedValue({
      ...groups,
      exceptions: [{ ...groups.exceptions[1], fingerprint: 'a/b?c=d&e' }],
    })
    render(<Exceptions />, { wrapper })
    fireEvent.click(await screen.findByText('NullReferenceException: Object reference not set to an instance of an object'))
    expect(await screen.findByTestId('where')).toHaveTextContent('/exceptions/detail?fingerprint=a%2Fb%3Fc%3Dd%26e')
  })

  it('explains an empty first-error list and survives an error', async () => {
    api.getExceptions.mockResolvedValue({ exceptions: [], total_count: 0, page: 1, page_size: 20 })
    render(<Exceptions />, { wrapper })
    fireEvent.click(screen.getByLabelText('First error of the session only'))
    expect(await screen.findByText('No exception was the first error of a session in this range')).toBeInTheDocument()
  })

  it('shows an error instead of a blank page when the request fails', async () => {
    api.getExceptions.mockRejectedValue(new Error('500'))
    render(<Exceptions />, { wrapper })
    expect(await screen.findByText('Could not load the exceptions.')).toBeInTheDocument()
  })
})

describe('Exceptions page (APM-8)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAppVersions.mockResolvedValue([])
    api.getContextKeys.mockResolvedValue({ keys: [] })
    api.getContextValues.mockResolvedValue({ key: '', values: [] })
  })

  it('shows each group state and filters by state', async () => {
    api.getExceptions.mockResolvedValue({
      ...groups,
      exceptions: [
        { ...groups.exceptions[0], status: 'resolved', resolved_in_version: '2.4' },
        { ...groups.exceptions[1], status: 'unresolved', regressed_in_version: '2.5' },
      ],
    })
    render(<Exceptions />, { wrapper })
    expect(await screen.findByText('Resolved in 2.4')).toBeInTheDocument()
    expect(screen.getByText('Regressed in 2.5')).toBeInTheDocument()
    expect(api.getExceptions.mock.lastCall?.[0].status).toBeUndefined()

    fireEvent.change(screen.getByLabelText('Status'), { target: { value: 'unresolved' } })
    await waitFor(() => expect(api.getExceptions).toHaveBeenLastCalledWith(expect.objectContaining({ status: 'unresolved', page: 1 })))
    fireEvent.change(screen.getByLabelText('Status'), { target: { value: '' } })
    await waitFor(() => expect(api.getExceptions.mock.lastCall?.[0].status).toBeUndefined())
  })

  it('shows no state for an older server', async () => {
    api.getExceptions.mockResolvedValue(groups)
    render(<Exceptions />, { wrapper })
    await screen.findByText('System.InvalidOperationException')
    expect(screen.queryByText('Unresolved', { selector: 'span' })).toBeNull()
  })
})
