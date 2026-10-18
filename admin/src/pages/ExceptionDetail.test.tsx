import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import type { ReactNode } from 'react'
import { FiltersProvider } from '../hooks/useFilters'
import { ExceptionDetail } from './ExceptionDetail'
import { normalizeOccurrence } from '../services/api'

const { api } = vi.hoisted(() => ({
  api: {
    getAppVersions: vi.fn(),
    getContextKeys: vi.fn(),
    getContextValues: vi.fn(),
    getExceptionDetail: vi.fn(),
    getExceptionOccurrences: vi.fn(),
    getExceptionDistribution: vi.fn(),
  },
}))

vi.mock('../services/api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../services/api')>()
  return { ...api, normalizeOccurrence: real.normalizeOccurrence }
})

// recharts needs ResizeObserver (not in jsdom): a stand-in that shows the points.
vi.mock('../components/TimeSeriesChart', () => ({
  TimeSeriesChart: ({ title, data }: { title: string; data?: { value: number }[] }) => (
    <div data-testid="chart">
      {title}: {data && data.length > 0 ? data.map((p) => p.value).join(',') : 'No data available'}
    </div>
  ),
}))

function renderAt(path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <FiltersProvider>
          <Routes>
            <Route path="/exceptions/detail" element={children} />
          </Routes>
        </FiltersProvider>
      </MemoryRouter>
    </QueryClientProvider>
  )
  return render(<ExceptionDetail />, { wrapper })
}

const busException =
  'System.InvalidOperationException: Room template not found for roomId=r-12\n  at Ozx.Rooms.RoomBuilder.Build (System.String roomId) [0x00000] in /Users/builder/ozx/Assets/Scripts/Rooms/RoomBuilder.cs:88 \n'

const detail = {
  fingerprint: 'fad75585e1523085',
  message: '[GameEventBus] Error handling event RoomEnteredEvent: ' + busException,
  count: 41,
  session_count: 3,
  first_seen: '2026-10-17T10:00:00Z',
  last_seen: '2026-10-18T10:00:10Z',
  fingerprint_version: 1,
  exception_type: 'System.InvalidOperationException',
  message_template: 'Room template not found for roomId=<v>',
  throw_site: 'Ozx.Rooms.RoomBuilder.Build (at /Users/builder/ozx/Assets/Scripts/Rooms/RoomBuilder.cs:88)',
  call_site: 'Ozx.Core.GameEventBus.Publish (at Assets/Scripts/Core/GameEventBus.cs:52)',
  first_error_sessions: 2,
  unhandled_count: 0,
  sessions_with_seq: 3,
  first_version: '2.2',
  last_version: '2.3',
  sample: {
    timestamp: '2026-10-18T10:00:10Z',
    event_id: 'e1',
    log_type: 'error',
    message: '[GameEventBus] Error handling event RoomEnteredEvent: ' + busException,
    stack: 'Ozx.Core.GameEventBus:Publish(RoomEnteredEvent)',
    embedded_exception: busException,
    client_fingerprint: 'fad75585e1523085',
    fingerprint_version: 1,
    inner_exceptions: ['Newtonsoft.Json.JsonReaderException: Unexpected character'],
    parsed: {
      kind: 'exception',
      wrapped: true,
      chain: [
        {
          type: 'System.InvalidOperationException',
          message: 'Room template not found for roomId=r-12',
          frames: [
            { method: 'Ozx.Rooms.RoomBuilder.Build', file: '/Users/builder/ozx/Assets/Scripts/Rooms/RoomBuilder.cs', line: 88, in_app: true, raw: 'at ...' },
            { method: 'System.Collections.Generic.List.ForEach', in_app: false, raw: 'at System...' },
          ],
        },
        { type: 'Newtonsoft.Json.JsonReaderException', message: 'Unexpected character', frames: [] },
      ],
      log_frames: [{ method: 'Ozx.Core.GameEventBus.Publish', file: 'Assets/Scripts/Core/GameEventBus.cs', line: 52, in_app: true, raw: 'x' }],
      message_template: '',
      throw_site: '',
      call_site: 'Ozx.Core.GameEventBus.Publish (at Assets/Scripts/Core/GameEventBus.cs:52)',
      fingerprint: 'fad75585e1523085',
      fingerprint_version: 1,
    },
  },
  occurrences: [],
  version_distribution: [],
  device_distribution: [],
  trend: { interval_seconds: 3600, points: [{ timestamp: '2026-10-18T10:00:00Z', count: 41, sessions: 3 }] },
}

const occurrence = normalizeOccurrence({
  timestamp: '2026-10-18T10:00:10.123Z',
  event_id: 'e1',
  count: 1,
  app_version: '2.3',
  build_type: 'release',
  platform: 'Android',
  device_model: 'SM-A336B',
  os_version: '14',
  scene: 'Main',
  session_id: '1729245600000_a1b2&x=1',
  device_id: 'dev 1',
  context: { roomId: 'r-12', planId: 'p-7', masterSeed: '184467440737' },
  breadcrumbs: [
    '[10:00:01.000] Scene: Main',
    '[10:00:05.000] Event: room_enter roomId=r-12',
    '[10:00:09.000] Warning: Asset not found: anim/arack_red (x30 until 10:00:09.900)',
    '[10:00:09.500] Error: <img src=x onerror=alert(1)>',
  ],
  error_seq: 1,
  session_ms: 10123,
  unhandled: '',
  ctx_merged: false,
})

describe('Exception detail page (APM-11)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAppVersions.mockResolvedValue([])
    api.getContextKeys.mockResolvedValue({ keys: [{ key: 'roomId', count: 9 }] })
    api.getContextValues.mockResolvedValue({ key: '', values: [] })
  })

  it('shows the stack with file and line, the call site, the first-error share, distribution and occurrences', async () => {
    api.getExceptionDetail.mockResolvedValue(detail)
    api.getExceptionOccurrences.mockResolvedValue({ fingerprint: detail.fingerprint, occurrences: [occurrence], total_count: 1, page: 1, page_size: 10 })
    api.getExceptionDistribution.mockResolvedValue({
      fingerprint: detail.fingerprint,
      group_by: 'app_version',
      total: 41,
      merged: 0,
      truncated: false,
      rows: [{ value: '2.3', count: 41, sessions: 3 }],
    })
    renderAt('/exceptions/detail?fingerprint=fad75585e1523085')

    expect(await screen.findByText('Ozx.Rooms.RoomBuilder.Build')).toBeInTheDocument()
    expect(screen.getByText('Ozx.Rooms.RoomBuilder.Build').closest('li')).toHaveTextContent('Ozx.Rooms.RoomBuilder.Build (/Users/builder/ozx/Assets/Scripts/Rooms/RoomBuilder.cs:88)')
    expect(screen.getByText(/caught and logged with an error message/)).toBeInTheDocument()
    expect(screen.getAllByText('Ozx.Core.GameEventBus.Publish (at Assets/Scripts/Core/GameEventBus.cs:52)').length).toBeGreaterThan(0)
    expect(screen.getByText('Inner exception:')).toBeInTheDocument()
    expect(screen.queryByTestId('no-file-line')).toBeNull()
    expect(screen.getByText('67% (2 of 3)')).toBeInTheDocument()
    expect(screen.getByText(/Oct 17, 2026 \d\d:00 · 2\.2/)).toBeInTheDocument()
    expect(api.getExceptionDetail).toHaveBeenCalledWith('fad75585e1523085', expect.objectContaining({ build_type: 'release' }))

    // Occurrence: context, session position, breadcrumbs by kind, link to the session.
    const card = await screen.findByTestId('occurrence')
    expect(within(card).getByText('first error of the session')).toBeInTheDocument()
    expect(within(card).getByText('10.1s into the session')).toBeInTheDocument()
    expect(within(card).getByText('184467440737')).toBeInTheDocument()
    expect(within(card).getByText('p-7')).toBeInTheDocument()
    const crumbs = within(card).getByLabelText('Breadcrumbs')
    expect(crumbs.querySelectorAll('[data-kind="event"]')).toHaveLength(1)
    expect(crumbs.querySelectorAll('[data-kind="warning"]')).toHaveLength(1)
    expect(within(crumbs).getByText('×30 until 10:00:09.900')).toBeInTheDocument()
    // Game text is text: no element is created from it.
    expect(within(crumbs).getByText('<img src=x onerror=alert(1)>')).toBeInTheDocument()
    expect(document.querySelector('img')).toBeNull()
    const link = within(card).getByText('Session time line at this moment').closest('a')!
    const href = new URL(link.getAttribute('href')!, 'http://x')
    expect(href.pathname).toBe('/sessions')
    expect(href.searchParams.get('session_id')).toBe('1729245600000_a1b2&x=1')
    expect(href.searchParams.get('at')).toBe('2026-10-18T10:00:10.123Z')
    expect(href.searchParams.get('start_time')).toBe('2026-10-18T09:59:00.000Z')
    const dev = new URL(within(card).getByText('Its sessions').closest('a')!.getAttribute('href')!, 'http://x')
    expect(dev.searchParams.get('device_id')).toBe('dev 1')
    expect(dev.searchParams.get('start_time')).toBe('2026-10-04T10:00:10.123Z')

    // Distribution by a context key: roomId, with the merged occurrences apart.
    api.getExceptionDistribution.mockResolvedValue({
      fingerprint: detail.fingerprint,
      group_by: 'ctx:roomId',
      total: 48,
      merged: 7,
      truncated: false,
      rows: [
        { value: 'r-1', count: 21, sessions: 1 },
        { value: 'r-2', count: 10, sessions: 1 },
        { value: 'r-3', count: 10, sessions: 1 },
      ],
    })
    fireEvent.change(screen.getByLabelText('Distribution by'), { target: { value: 'key:' } })
    await waitFor(() =>
      expect(api.getExceptionDistribution).toHaveBeenLastCalledWith(detail.fingerprint, { kind: 'key', key: 'roomId' }, expect.anything())
    )
    expect(await screen.findByText('r-3')).toBeInTheDocument()
    expect(screen.getByText(/7 of 48 occurrences are not in the table/)).toBeInTheDocument()
  })

  it('renders a group of an old SDK and an old server without blank fields or errors', async () => {
    // Only what a pre-APM-4 row and a server before APM-11 return.
    api.getExceptionDetail.mockResolvedValue({
      ...detail,
      exception_type: '',
      throw_site: '',
      call_site: '',
      fingerprint_version: 0,
      first_error_sessions: undefined,
      sessions_with_seq: undefined,
      first_version: '',
      trend: { interval_seconds: 0, points: [] },
      sample: {
        ...detail.sample,
        embedded_exception: '',
        inner_exceptions: [],
        parsed: { kind: '', wrapped: false, chain: [], log_frames: [], message_template: '', throw_site: '', call_site: '', fingerprint: '', fingerprint_version: 0 },
      },
    })
    api.getExceptionOccurrences.mockResolvedValue({
      fingerprint: detail.fingerprint,
      occurrences: [normalizeOccurrence({ timestamp: '2026-10-18T10:00:10Z', count: 3 })],
      total_count: 1,
      page: 1,
      page_size: 10,
    })
    api.getExceptionDistribution.mockResolvedValue({ fingerprint: '', group_by: '', total: 0, merged: 0, truncated: false, rows: [] })
    renderAt('/exceptions/detail?fingerprint=old')

    expect(await screen.findByText(/grouped by an SDK before APM-4/)).toBeInTheDocument()
    expect(screen.getByText('unknown')).toBeInTheDocument()
    expect(screen.getByText('No exception chain could be parsed.')).toBeInTheDocument()
    expect(screen.getByTestId('chart')).toHaveTextContent('Occurrences over time: No data available')
    const card = await screen.findByTestId('occurrence')
    expect(within(card).getByText('No game context.')).toBeInTheDocument()
    expect(within(card).getByText(/No breadcrumbs/)).toBeInTheDocument()
    expect(within(card).queryByText('Session time line at this moment')).toBeNull()
    expect(screen.getByText('No occurrences.')).toBeInTheDocument() // the distribution
  })

  it('explains a group that the filters exclude (404)', async () => {
    api.getExceptionDetail.mockRejectedValue({ response: { status: 404 } })
    api.getExceptionOccurrences.mockResolvedValue({ fingerprint: '', occurrences: [], total_count: 0, page: 1, page_size: 10 })
    api.getExceptionDistribution.mockResolvedValue({ fingerprint: '', group_by: '', total: 0, merged: 0, truncated: false, rows: [] })
    renderAt('/exceptions/detail?fingerprint=gone')
    expect(await screen.findByText(/No occurrence of this group matches the current filters/)).toBeInTheDocument()
  })

  it('says how to get file and line when an IL2CPP stack has none (APM-7)', async () => {
    const noLines = {
      ...detail,
      sample: {
        ...detail.sample,
        parsed: {
          ...detail.sample.parsed,
          chain: [{ type: 'System.NullReferenceException', message: 'x', frames: [{ method: 'Ozx.Rooms.RoomBuilder.Build', in_app: true, raw: 'Ozx.Rooms.RoomBuilder:Build(String)' }] }],
          log_frames: [{ method: 'Ozx.Core.GameEventBus.Publish', in_app: true, raw: 'x' }],
        },
      },
    }
    api.getExceptionDetail.mockResolvedValue(noLines)
    api.getExceptionOccurrences.mockResolvedValue({ fingerprint: '', occurrences: [], total_count: 0, page: 1, page_size: 10 })
    api.getExceptionDistribution.mockResolvedValue({ fingerprint: '', group_by: '', total: 0, merged: 0, truncated: false, rows: [] })
    renderAt('/exceptions/detail?fingerprint=fad75585e1523085')
    expect(await screen.findByTestId('no-file-line')).toHaveTextContent('Method Name, File Name, and Line Number')
  })

  it('asks for a fingerprint', () => {
    renderAt('/exceptions/detail')
    expect(screen.getByText('No exception group selected.')).toBeInTheDocument()
    expect(api.getExceptionDetail).not.toHaveBeenCalled()
  })
})
