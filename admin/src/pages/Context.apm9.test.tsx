import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { FiltersProvider } from '../hooks/useFilters'
import { Context } from './Context'

const { api } = vi.hoisted(() => ({
  api: {
    getAppVersions: vi.fn(),
    getContextKeys: vi.fn(),
    getContextValues: vi.fn(),
    getContextDistribution: vi.fn(),
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

describe('Context page, APM-9: count ranges, jank rate, tails', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAppVersions.mockResolvedValue([])
    api.getContextValues.mockResolvedValue({ key: '', values: [] })
    api.getContextKeys.mockResolvedValue({ keys: [{ key: 'aliveEnemies', count: 9 }] })
  })

  it('shows p05 / p99 and groups numbers into count ranges (no filter on a range)', async () => {
    api.getContextDistribution.mockResolvedValue({
      key: 'aliveEnemies',
      metric: 'fps',
      unit: 'fps',
      total: 60,
      truncated: false,
      bucket: 'count',
      rows: [
        { value: '6-10', count: 40, sessions: 1, p05: 30, p50: 35, p90: 40, p95: 40, p99: 40 },
        { value: 'many', count: 20, sessions: 1, p05: 50, p50: 50, p90: 50, p95: 50, p99: 50 },
      ],
    })
    render(<Context />, { wrapper })
    expect(await screen.findByText('p05')).toBeInTheDocument()
    expect(screen.getByText('p99')).toBeInTheDocument()
    expect(screen.getByText('30.0')).toBeInTheDocument()

    fireEvent.click(screen.getByLabelText('Group numeric values into count ranges'))
    await waitFor(() =>
      expect(api.getContextDistribution).toHaveBeenLastCalledWith('aliveEnemies', 'fps', expect.objectContaining({ bucket: 'count' }))
    )
    // "6-10" is a range: only the plain value gets a Filter button.
    expect(await screen.findAllByText('Filter')).toHaveLength(1)
  })

  it('shows janks per minute of play', async () => {
    api.getContextDistribution.mockResolvedValue({
      key: 'aliveEnemies',
      metric: 'fps',
      unit: 'fps',
      total: 1,
      truncated: false,
      rows: [{ value: '1', count: 1, sessions: 1, p50: 55, p95: 59 }],
    })
    render(<Context />, { wrapper })
    // An older server: no p05/p99 columns.
    expect(await screen.findByText('59.0')).toBeInTheDocument()
    expect(screen.queryByText('p05')).toBeNull()

    api.getContextDistribution.mockResolvedValue({
      key: 'aliveEnemies',
      metric: 'jank_rate',
      unit: 'janks/min',
      total: 9,
      truncated: false,
      rows: [{ value: 'boss', count: 8, sessions: 1, p50: 0, p95: 0, minutes: 1.3333, rate: 6 }],
    })
    fireEvent.change(screen.getByLabelText('Metric'), { target: { value: 'jank_rate' } })
    expect(await screen.findByText('Janks / min')).toBeInTheDocument()
    expect(screen.getByText('6.00')).toBeInTheDocument()
    expect(screen.getByText('1.3')).toBeInTheDocument()
    expect(screen.queryByText('p50')).toBeNull()
    expect(api.getContextDistribution).toHaveBeenLastCalledWith('aliveEnemies', 'jank_rate', expect.anything())
  })
})
