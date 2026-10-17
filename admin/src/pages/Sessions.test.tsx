import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import type { ReactNode } from 'react'
import { Sessions } from './Sessions'
import type { SessionTimeline, TimelineItem } from '../types/api'

const { api } = vi.hoisted(() => ({
  api: {
    getSessionTimeline: vi.fn(),
    getDeviceSessions: vi.fn(),
  },
}))

vi.mock('../services/api', () => api)

vi.mock('../components/TimeSeriesChart', () => ({
  TimeSeriesChart: ({ title, data }: { title: string; data?: { value: number }[] }) => (
    <div data-testid="chart">
      {title}: {(data ?? []).map((p) => p.value).join(',')}
    </div>
  ),
}))

function renderAt(path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/sessions" element={children} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  )
  return render(<Sessions />, { wrapper })
}

const T0 = Date.parse('2026-10-18T10:00:00.000Z')
const at = (ms: number) => new Date(T0 + ms).toISOString()

// The time line of the "room only half built" session, as the server returns it.
const items: TimelineItem[] = [
  { time: at(0), type: 'context', source: 'startup', changes: [{ key: 'levelId', from: '', to: '3' }] },
  { time: at(0), type: 'startup', phase1_ms: 900, phase2_ms: 400, tti_ms: 1200 },
  { time: at(1000), type: 'scene_load', scene_name: 'Main', load_ms: 350, activate_ms: 20 },
  { time: at(5000), type: 'context', source: 'custom_event', changes: [{ key: 'roomId', from: '', to: 'r-11' }] },
  { time: at(5000), type: 'custom_event', name: 'room_enter', count: 1, props: { roomId: 'r-11' }, seq: 30 },
  { time: at(6000), type: 'custom_event', name: 'room_ready', count: 1, props: { roomId: 'r-11' }, seq: 31 },
  { time: at(6000), type: 'custom_metric', name: 'room_switch_ms', unit: 'ms', count: 2, sum: 1340, min: 640, max: 700, seq: 32 },
  { time: at(9000), type: 'breadcrumb', kind: 'warning', text: 'Asset not found: anim/arack_red', repeats: 30, repeat_until: at(14000), source: 'exception' },
  { time: at(10000), type: 'context', source: 'custom_event', changes: [{ key: 'roomId', from: 'r-11', to: 'r-12' }] },
  { time: at(10000), type: 'custom_event', name: 'room_enter', count: 1, props: { roomId: 'r-12' }, seq: 33 },
  {
    time: at(10000),
    type: 'exception',
    fingerprint: 'fad7/x?y',
    exception_type: 'System.InvalidOperationException',
    message: '[GameEventBus] Error handling event RoomEnteredEvent: System.InvalidOperationException: Room template not found\n  at X',
    count: 1,
    error_seq: 1,
    session_ms: 10000,
    throw_site: 'Ozx.Rooms.RoomBuilder.Build (at RoomBuilder.cs:88)',
    seq: 34,
  },
  { time: at(10500), type: 'exception', fingerprint: 'fad7/x?y', exception_type: 'System.InvalidOperationException', message: 'm', count: 40, error_seq: 2, seq: 35 },
  { time: at(12000), type: 'custom_event', name: 'enemy_killed', count: 15, aggregated: true, seq: 36 },
  { time: at(14900), type: 'breadcrumb', kind: 'error', text: '<script>alert(1)</script>', source: 'jank' },
  { time: at(15000), type: 'jank', duration_ms: 120, max_frame_ms: 95, recent_gc_count: 2, recent_events: ['[10:00:14.900] Error: <script>alert(1)</script>'] },
]

const timeline: SessionTimeline = {
  session_id: 'S1 half&x',
  start_time: at(-60000),
  end_time: at(86400000),
  found: true,
  session: {
    session_id: 'S1 half&x',
    first_seen: at(0),
    last_seen: at(20500),
    started_at: at(0),
    device_id: 'dev/S1',
    app_version: '2.3',
    platform: 'Android',
    device_model: 'SM-A336B',
    os_version: '14',
    build_type: 'release',
    rows: 31,
  },
  items,
  counts: { custom_event: 4, custom_metric: 1, exception: 2, crash: 0, jank: 1, scene_load: 1, startup: 1, context: 3, breadcrumb: 2 },
  truncated: [],
  duplicate_breadcrumbs: 6,
  perf: [],
  perf_interval_seconds: 0,
}

function renderedTypes(): string[] {
  const list = screen.getByLabelText('Session time line')
  return Array.from(list.querySelectorAll('li[data-type]')).map((li) => li.getAttribute('data-type') || '')
}

describe('Sessions page (APM-15)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('shows the session in time order, anchored at the occurrence: room_enter, the exception, no room_ready, then the end', async () => {
    api.getSessionTimeline.mockResolvedValue(timeline)
    const q = new URLSearchParams({ session_id: 'S1 half&x', at: at(10000), start_time: at(-60000), end_time: at(86400000) })
    renderAt('/sessions?' + q.toString())

    expect(await screen.findByText('Session started')).toBeInTheDocument()
    expect(api.getSessionTimeline).toHaveBeenCalledWith({ session_id: 'S1 half&x', at: at(10000), start_time: at(-60000), end_time: at(86400000) })
    expect(renderedTypes()).toEqual(items.map((i) => i.type))

    // The anchor is the exception at that moment.
    const anchor = screen.getByLabelText('Session time line').querySelector('[data-anchor="true"]')!
    expect(anchor.getAttribute('data-type')).toBe('exception')
    expect(within(anchor as HTMLElement).getByText('first error')).toBeInTheDocument()
    expect(within(anchor as HTMLElement).getByText('details').closest('a')!.getAttribute('href')).toBe(
      '/exceptions/detail?fingerprint=fad7%2Fx%3Fy'
    )

    // room_enter of r-12 and no room_ready after it; the last marker follows.
    const events = Array.from(screen.getByLabelText('Session time line').querySelectorAll('li[data-type="custom_event"]')).map(
      (li) => li.textContent
    )
    expect(events).toEqual(['10:00:05.000+5.0sEventroom_enter roomId=r-11', '10:00:06.000+6.0sEventroom_ready roomId=r-11', '10:00:10.000+10.0sEventroom_enter roomId=r-12', '10:00:12.000+12.0sEventenemy_killed ×15 calls (counted)'])
    expect(screen.getByText(/Last data from this session/)).toBeInTheDocument()
    expect(screen.getByText('r-12')).toBeInTheDocument() // the context change
    expect(screen.getByText(/window aggregate/)).toBeInTheDocument()
    expect(screen.getByText('×30 until 10:00:14.000')).toBeInTheDocument()

    // Game text stays text.
    expect(screen.getAllByText('<script>alert(1)</script>').length).toBeGreaterThan(0)
    expect(document.querySelector('script')).toBeNull()

    // The device's other sessions, encoded.
    expect(screen.getByText('Other sessions of this device').closest('a')!.getAttribute('href')).toBe('/sessions?device_id=dev%2FS1')

    // Filter by type: hide the log lines and breadcrumbs, then the context.
    fireEvent.click(screen.getByLabelText('Show Log / breadcrumb'))
    fireEvent.click(screen.getByLabelText('Show Context'))
    expect(renderedTypes()).toEqual(items.filter((i) => i.type !== 'breadcrumb' && i.type !== 'context').map((i) => i.type))
  })

  it('adds the performance track on request', async () => {
    api.getSessionTimeline.mockResolvedValue(timeline)
    renderAt('/sessions?session_id=S1')
    const box = await screen.findByLabelText('Performance track')
    api.getSessionTimeline.mockResolvedValue({ ...timeline, perf: [{ time: at(0), samples: 5, fps_avg: 55, fps_min: 50, frame_ms_max: 20, mem_mb_max: 400 }], perf_interval_seconds: 5 })
    fireEvent.click(box)
    await waitFor(() => expect(api.getSessionTimeline).toHaveBeenLastCalledWith({ session_id: 'S1', perf: '1' }))
    expect(await screen.findByText('FPS (average per 5 s): 55')).toBeInTheDocument()
  })

  it('says when the session has no data in the range, and shows a bad request', async () => {
    api.getSessionTimeline.mockResolvedValue({ ...timeline, found: false, items: [], session: { ...timeline.session, first_seen: '', last_seen: '' } })
    renderAt('/sessions?session_id=nope')
    expect(await screen.findByText(/Data is kept 30 days/)).toBeInTheDocument()
  })

  it('shows the error text of a 400', async () => {
    api.getSessionTimeline.mockRejectedValue({ response: { status: 400, data: 'range longer than 31 days\n' } })
    renderAt('/sessions?session_id=x')
    expect(await screen.findByText(/range longer than 31 days/)).toBeInTheDocument()
  })

  it('renders a session of an old SDK: no context, no breadcrumbs, no session start', async () => {
    api.getSessionTimeline.mockResolvedValue({
      ...timeline,
      session: { ...timeline.session, started_at: undefined, device_id: '' },
      items: [{ time: at(0), type: 'exception', message: 'NullReferenceException: x', count: 3 }, { time: at(0), type: 'breadcrumb', kind: 'text', text: 'x', time_unknown: true, source: 'jank' }],
      counts: {},
    })
    renderAt('/sessions?session_id=old')
    expect(await screen.findByText('NullReferenceException: x', { selector: 'span' })).toBeInTheDocument()
    expect(screen.queryByText('Session started')).toBeNull()
    expect(screen.queryByText('Other sessions of this device')).toBeNull()
    expect(renderedTypes()).toEqual(['exception', 'breadcrumb'])
  })

  it('opens a session or a device from the search box', async () => {
    api.getSessionTimeline.mockResolvedValue(timeline)
    api.getDeviceSessions.mockResolvedValue({
      device_id: 'dev-1',
      start_time: at(0),
      end_time: at(1000),
      truncated: false,
      sessions: [{ session_id: 'S&1', first_seen: at(0), last_seen: at(20500), rows: 31, exceptions: 41, crashes: 0, app_version: '2.3', platform: 'Android', device_model: 'm', build_type: 'release' }],
    })
    renderAt('/sessions')
    expect(screen.getByText(/enter a session id or a device id/)).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('Device id'), { target: { value: ' dev-1 ' } })
    fireEvent.click(screen.getByText('List sessions'))
    await waitFor(() => expect(api.getDeviceSessions).toHaveBeenCalledWith('dev-1', expect.objectContaining({ start_time: expect.any(String) })))
    const link = (await screen.findByText('S&1')).closest('a')!
    const href = new URL(link.getAttribute('href')!, 'http://x')
    expect(href.searchParams.get('session_id')).toBe('S&1')

    fireEvent.change(screen.getByLabelText('Session id'), { target: { value: 'S1 half&x' } })
    fireEvent.click(screen.getByText('Open time line'))
    await waitFor(() => expect(api.getSessionTimeline).toHaveBeenCalledWith(expect.objectContaining({ session_id: 'S1 half&x' })))
  })
})
