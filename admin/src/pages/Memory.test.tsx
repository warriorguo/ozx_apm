import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { FiltersProvider } from '../hooks/useFilters'
import { Memory, mb } from './Memory'

const { api } = vi.hoisted(() => ({
  api: {
    getAppVersions: vi.fn(),
    getContextKeys: vi.fn(),
    getContextValues: vi.fn(),
    getMemoryBreakdown: vi.fn(),
    getRunPeaks: vi.fn(),
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

const fig = (p50: number, p95: number, max: number, rows = 3) => ({ rows, p50, p95, max })

const peakRow = (value: string, extra = {}) => ({
  value,
  runs: 5,
  with_value: 5,
  sessions: 5,
  devices: 4,
  crashed_runs: 1,
  oom_runs: 1,
  killed_runs: 0,
  p50: 1400,
  p90: 1600,
  p99: 4800,
  max: 4800,
  avg_duration_ms: 600000,
  avg_growth_mb: 450,
  ...extra,
})

describe('Memory page (APM-9)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAppVersions.mockResolvedValue([])
    api.getContextKeys.mockResolvedValue({ keys: [{ key: 'stageType', count: 9 }] })
    api.getContextValues.mockResolvedValue({ key: '', values: [] })
  })

  it('shows the breakdown, the run peaks and the histogram with OOM runs', async () => {
    api.getMemoryBreakdown.mockResolvedValue({
      group_by: 'app_version',
      truncated: false,
      rows: [
        {
          value: '3.0',
          samples: 4,
          sessions: 2,
          figures: { mem_mb: fig(81, 82, 82, 4), mem_rss_mb: fig(900, 1000, 1000), mem_alloc_mb: fig(0, 0, 0, 0) },
        },
      ],
    })
    api.getRunPeaks.mockResolvedValue({
      metric: 'peak_rss',
      group_by: 'app_version',
      truncated: false,
      rows: [peakRow('3.0')],
      buckets: [
        { low: 0, high: 256, runs: 0, oom: 0 },
        { low: 768, high: 1024, runs: 3, oom: 0 },
        { low: 4096, high: 0, runs: 1, oom: 1 },
      ],
    })
    render(<Memory />, { wrapper })

    expect(await screen.findByTestId('fig-3.0-mem_rss_mb')).toHaveTextContent('900 MB / 1000 MB')
    expect(screen.getByTestId('fig-3.0-mem_rss_mb')).toHaveTextContent('max 1000 MB · 3')
    expect(screen.getByTestId('fig-3.0-mem_alloc_mb')).toHaveTextContent('-') // no samples with it
    expect(screen.getByTestId('fig-3.0-mem_gfx_mb')).toHaveTextContent('-') // absent
    expect(await screen.findAllByText('4.69 GB')).toHaveLength(2) // p99 and max
    expect(screen.getByText('+450 MB')).toBeInTheDocument()
    const bars = screen.getAllByTestId('peak-bucket')
    expect(bars).toHaveLength(3)
    expect(bars[2]).toHaveTextContent('≥ 4.00 GB')
    expect(bars[2]).toHaveTextContent('1 runs · 1 OOM')
    expect(bars[0]).toHaveTextContent('0 – 256 MB')
    expect(api.getMemoryBreakdown).toHaveBeenCalledWith({ kind: 'by', key: 'app_version' }, expect.objectContaining({ build_type: 'release' }))

    fireEvent.change(screen.getByLabelText('Peak metric'), { target: { value: 'peak_alloc' } })
    await waitFor(() => expect(api.getRunPeaks).toHaveBeenLastCalledWith('peak_alloc', { kind: 'by', key: 'app_version' }, expect.anything()))

    fireEvent.change(screen.getByLabelText('Group by'), { target: { value: 'key:stageType' } })
    await waitFor(() => expect(api.getMemoryBreakdown).toHaveBeenLastCalledWith({ kind: 'key', key: 'stageType' }, expect.anything()))
  })

  it('renders game text as text, and empty states', async () => {
    api.getMemoryBreakdown.mockResolvedValue({ group_by: '', truncated: false, rows: [] })
    api.getRunPeaks.mockResolvedValue({
      metric: 'peak_rss',
      group_by: '',
      truncated: true,
      rows: [peakRow('<img src=x onerror=alert(1)>'), peakRow('x'.repeat(300), { oom_runs: undefined })],
      buckets: [],
    })
    const { container } = render(<Memory />, { wrapper })
    expect(await screen.findByText('No perf samples in this range.')).toBeInTheDocument()
    expect(await screen.findByText('<img src=x onerror=alert(1)>')).toBeInTheDocument()
    expect(container.querySelector('img')).toBeNull()
    const long = screen.getByTitle('x'.repeat(300))
    expect(long.textContent).toBe('x'.repeat(120) + '…')
  })

  it('explains missing runs and shows errors', async () => {
    api.getMemoryBreakdown.mockRejectedValue(new Error('500'))
    api.getRunPeaks.mockResolvedValue({ metric: 'peak_rss', group_by: '', truncated: false, rows: [], buckets: [] })
    render(<Memory />, { wrapper })
    expect(await screen.findByText('Could not load the memory breakdown.')).toBeInTheDocument()
    expect(await screen.findByText(/No runs in this range/)).toBeInTheDocument()
  })

  it('formats MB', () => {
    expect(mb(0)).toBe('-')
    expect(mb(NaN)).toBe('-')
    expect(mb(512.4)).toBe('512 MB')
    expect(mb(2048)).toBe('2.00 GB')
  })
})
