import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { FiltersProvider } from '../hooks/useFilters'
import { Dashboard, crashRateText, crashRateIsHigh, CRASH_RATE_DANGER } from './Dashboard'
import type { DashboardSummary } from '../types/api'

const { api } = vi.hoisted(() => ({
  api: {
    getSummary: vi.fn(),
    getTimeSeries: vi.fn(),
    getAppVersions: vi.fn(),
    getContextKeys: vi.fn(),
    getContextValues: vi.fn(),
  },
}))

vi.mock('../services/api', () => api)
vi.mock('../components/TimeSeriesChart', () => ({
  TimeSeriesChart: ({ title }: { title: string }) => <div>{title}</div>,
}))

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return (
    <QueryClientProvider client={client}>
      <FiltersProvider>{children}</FiltersProvider>
    </QueryClientProvider>
  )
}

// The numbers of the APM-22 browser check: 2 crashes in 25 sessions.
const summary = (extra: Partial<DashboardSummary> = {}): DashboardSummary => ({
  time_range: { start: '', end: '' },
  total_sessions: 25,
  total_events: 500,
  crash_count: 2,
  crashed_sessions: 2,
  crashed_session_rate: 0.08,
  crash_rate: 80, // deprecated: per 1000 sessions
  exception_count: 3,
  jank_count: 1,
  avg_fps: 58,
  avg_startup_ms: 1200,
  top_versions: [
    { version: '1.2.0', session_count: 20, crash_count: 0, crashed_sessions: 0, crashed_session_rate: 0, crash_rate: 0 },
    { version: '1.1.0', session_count: 5, crash_count: 3, crashed_sessions: 2, crashed_session_rate: 0.4, crash_rate: 600 },
  ],
  top_platforms: [],
  ...extra,
})

describe('crash rate units (APM-22)', () => {
  it('formats the crashed-session fraction as a percentage', () => {
    expect(crashRateText(0.08)).toBe('8.00%')
    expect(crashRateText(0)).toBe('0.00%')
    expect(crashRateText(1)).toBe('100.00%')
    expect(crashRateText(undefined)).toBe('n/a')
    expect(crashRateText(NaN)).toBe('n/a')
  })

  it('highlights above 1% of sessions crashed', () => {
    expect(CRASH_RATE_DANGER).toBe(0.01)
    expect(crashRateIsHigh(0.08)).toBe(true)
    expect(crashRateIsHigh(0.01)).toBe(false)
    expect(crashRateIsHigh(0.005)).toBe(false)
    expect(crashRateIsHigh(undefined)).toBe(false)
  })
})

describe('Dashboard (APM-22)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getTimeSeries.mockResolvedValue({ metric: '', data: [] })
    api.getAppVersions.mockResolvedValue([])
    api.getContextKeys.mockResolvedValue({ keys: [] })
    api.getContextValues.mockResolvedValue({ key: '', values: [] })
  })

  it('shows 2 crashed sessions of 25 as 8.00%, not 8000.00%', async () => {
    api.getSummary.mockResolvedValue(summary())
    render(<Dashboard />, { wrapper })
    expect(await screen.findByText('8.00%')).toBeInTheDocument()
    expect(screen.getByText('2 of 25 sessions crashed')).toBeInTheDocument()
    expect(screen.queryByText(/8000/)).not.toBeInTheDocument()
    expect(screen.getByText('Crash Rate (sessions)').parentElement?.nextElementSibling).toHaveClass('bg-red-500')
    expect(screen.getByText('Crash Reports')).toBeInTheDocument()
    expect(screen.getByText('Every type, also ANRs and force-stops')).toBeInTheDocument()
  })

  it('shows each version with its crashed-session rate', async () => {
    api.getSummary.mockResolvedValue(summary())
    render(<Dashboard />, { wrapper })
    const ok = await screen.findByTestId('version-crash-rate-1.2.0')
    expect(ok).toHaveTextContent('0.00% sessions crashed')
    expect(ok).toHaveClass('text-green-500')
    const bad = screen.getByTestId('version-crash-rate-1.1.0')
    expect(bad).toHaveTextContent('40.00% sessions crashed')
    expect(bad).toHaveClass('text-red-500')
  })

  it('is green at 0.5% and never reads the per-1000 crash_rate as a fraction', async () => {
    api.getSummary.mockResolvedValue(
      summary({ total_sessions: 1000, crashed_sessions: 5, crashed_session_rate: 0.005, crash_count: 7, crash_rate: 7 }),
    )
    render(<Dashboard />, { wrapper })
    expect(await screen.findByText('0.50%')).toBeInTheDocument()
    expect(screen.queryByText('700.00%')).not.toBeInTheDocument()
    expect(screen.getByText('Crash Rate (sessions)').parentElement?.nextElementSibling).toHaveClass('bg-green-500')
  })

  it('says n/a against a server before APM-22', async () => {
    const old = summary({ top_versions: [{ version: '1.0', session_count: 25, crash_count: 2, crash_rate: 80 }] })
    delete old.crashed_sessions
    delete old.crashed_session_rate
    api.getSummary.mockResolvedValue(old)
    render(<Dashboard />, { wrapper })
    expect(await screen.findByText('n/a')).toBeInTheDocument()
    expect(screen.getByText('2 crash reports')).toBeInTheDocument()
    expect(screen.getByTestId('version-crash-rate-1.0')).toHaveTextContent('n/a sessions crashed')
    expect(screen.queryByText(/8000/)).not.toBeInTheDocument()
  })
})
