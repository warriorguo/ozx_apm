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

describe('Context page (APM-5)', () => {
  beforeEach(() => {
    api.getAppVersions.mockResolvedValue([])
    api.getContextValues.mockResolvedValue({ key: '', values: [] })
  })

  it('explains an empty context (legacy data) instead of failing', async () => {
    api.getContextKeys.mockResolvedValue({ keys: [] })
    api.getContextDistribution.mockResolvedValue({ key: '', metric: 'fps', unit: '', total: 0, truncated: false, rows: [] })
    render(<Context />, { wrapper })
    expect(await screen.findByText(/No game context in this range/)).toBeInTheDocument()
    expect(api.getContextDistribution).not.toHaveBeenCalled()
  })

  it('shows an error when the keys cannot be loaded', async () => {
    api.getContextKeys.mockRejectedValue(new Error('500'))
    render(<Context />, { wrapper })
    expect(await screen.findByText(/Could not load the context keys/)).toBeInTheDocument()
  })

  it('groups by the most common key and adds a filter from a row', async () => {
    api.getContextKeys.mockResolvedValue({ keys: [{ key: 'levelId', count: 9 }, { key: 'stageType', count: 4 }] })
    api.getContextDistribution.mockResolvedValue({
      key: 'levelId',
      metric: 'fps',
      unit: 'fps',
      total: 4,
      truncated: true,
      rows: [
        { value: '1', count: 2, sessions: 1, p50: 55, p95: 59.5 },
        { value: '', count: 1, sessions: 1, p50: 40, p95: 40 },
        { value: '2', count: 1, sessions: 1, p50: 30, p95: 30 },
      ],
    })
    render(<Context />, { wrapper })

    expect(await screen.findByText('(not set)')).toBeInTheDocument()
    expect(screen.getByText('50.0%')).toBeInTheDocument()
    expect(screen.getByText('59.5')).toBeInTheDocument()
    expect(screen.getByText(/most frequent values/)).toBeInTheDocument()
    expect(api.getContextDistribution).toHaveBeenCalledWith(
      'levelId',
      'fps',
      expect.objectContaining({ build_type: 'release' })
    )

    fireEvent.click(screen.getAllByText('Filter')[0])
    expect(await screen.findByText('levelId = 1')).toBeInTheDocument()
    await waitFor(() =>
      expect(api.getContextDistribution).toHaveBeenLastCalledWith(
        'levelId',
        'fps',
        expect.objectContaining({ ctx: ['levelId:1'] })
      )
    )

    // Another metric: counts only, no percentile columns.
    api.getContextDistribution.mockResolvedValue({
      key: 'levelId',
      metric: 'exceptions',
      unit: '',
      total: 3,
      truncated: false,
      rows: [{ value: '1', count: 3, sessions: 2, p50: 0, p95: 0 }],
    })
    fireEvent.change(screen.getByLabelText('Metric'), { target: { value: 'exceptions' } })
    await waitFor(() => expect(screen.queryByText('p50')).not.toBeInTheDocument())
    expect(await screen.findByText('100.0%')).toBeInTheDocument()
  })
})
