import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { FiltersProvider } from '../hooks/useFilters'
import { Custom, formatMetric } from './Custom'

const { api } = vi.hoisted(() => ({
  api: {
    getAppVersions: vi.fn(),
    getContextKeys: vi.fn(),
    getCustomEvents: vi.fn(),
    getCustomMetrics: vi.fn(),
    getCustomDistribution: vi.fn(),
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

const stat = (value: string, count: number, p50: number, p95: number) => ({
  value,
  count,
  sessions: 3,
  avg: p50,
  min: 1,
  max: p95 * 2,
  p50,
  p90: p95,
  p95,
  p99: p95,
})

describe('Custom page (APM-6)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAppVersions.mockResolvedValue([])
    api.getContextKeys.mockResolvedValue({ keys: [{ key: 'levelId', count: 9 }, { key: 'roomId', count: 9 }] })
  })

  it('explains an empty range', async () => {
    api.getCustomMetrics.mockResolvedValue({ metrics: [], truncated: false })
    render(<Custom />, { wrapper })
    expect(await screen.findByText(/No custom metrics in this range/)).toBeInTheDocument()
    expect(api.getCustomDistribution).not.toHaveBeenCalled()
  })

  // APM-1 verification 7: the room switch time p50/p95 per roomId.
  it('shows metric percentiles per roomId and filters from a row', async () => {
    api.getCustomMetrics.mockResolvedValue({
      metrics: [{ ...stat('', 405, 846, 2300), name: 'room_switch_ms', unit: 'ms' }],
      truncated: false,
    })
    api.getCustomDistribution.mockResolvedValue({
      name: 'room_switch_ms',
      group_by: 'ctx:roomId',
      unit: 'ms',
      total: 414,
      merged: 9,
      truncated: false,
      rows: [stat('r-1', 135, 396, 848), stat('r-3', 135, 2394, 5108)],
    })
    render(<Custom />, { wrapper })

    expect(await screen.findByText('r-3')).toBeInTheDocument()
    expect(screen.getAllByText('5.1s').length).toBeGreaterThan(0)
    expect(screen.getAllByText('848ms').length).toBeGreaterThan(0)
    expect(screen.getByText(/9 samples of 414 are not in the table/)).toBeInTheDocument()
    expect(api.getCustomDistribution).toHaveBeenCalledWith(
      'metrics',
      'room_switch_ms',
      { kind: 'key', key: 'roomId' },
      expect.objectContaining({ build_type: 'release' })
    )
    expect(api.getContextKeys).toHaveBeenCalledWith(expect.objectContaining({ source: 'custom_metrics' }))
    // The default grouping waits for the keys: no request by app_version first.
    expect(api.getCustomDistribution.mock.calls[0][2]).toEqual({ kind: 'key', key: 'roomId' })

    fireEvent.click(screen.getAllByText('Filter')[0])
    expect(await screen.findByText('roomId = r-1')).toBeInTheDocument()
    await waitFor(() =>
      expect(api.getCustomDistribution).toHaveBeenLastCalledWith(
        'metrics',
        'room_switch_ms',
        { kind: 'key', key: 'roomId' },
        expect.objectContaining({ ctx: ['roomId:r-1'] })
      )
    )

    // Version comparison.
    fireEvent.change(screen.getByLabelText('Group by'), { target: { value: 'by:app_version' } })
    await waitFor(() =>
      expect(api.getCustomDistribution).toHaveBeenLastCalledWith(
        'metrics',
        'room_switch_ms',
        { kind: 'by', key: 'app_version' },
        expect.anything()
      )
    )
  })

  it('shows event counts and groups them by a property', async () => {
    api.getCustomMetrics.mockResolvedValue({ metrics: [], truncated: false })
    api.getCustomEvents.mockResolvedValue({
      events: [{ name: 'room_enter', count: 14, sessions: 2, devices: 2, last_seen: '2026-10-18T10:00:00Z' }],
      truncated: false,
    })
    api.getCustomDistribution.mockResolvedValue({
      name: 'room_enter',
      group_by: 'prop:seed',
      unit: '',
      total: 14,
      merged: 11,
      truncated: false,
      rows: [stat('1', 2, 0, 0), stat('2', 1, 0, 0)],
    })
    render(<Custom />, { wrapper })
    fireEvent.click(await screen.findByRole('tab', { name: 'Events' }))
    expect(await screen.findByText('room_enter', { selector: 'td' })).toBeInTheDocument()

    fireEvent.change(await screen.findByLabelText('Group by'), { target: { value: 'prop:' } })
    fireEvent.change(screen.getByLabelText('Property key'), { target: { value: 'seed' } })
    await waitFor(() =>
      expect(api.getCustomDistribution).toHaveBeenLastCalledWith(
        'events',
        'room_enter',
        { kind: 'prop', key: 'seed' },
        expect.anything()
      )
    )
    expect(await screen.findByText(/properties were not kept/)).toBeInTheDocument()
    expect(screen.queryByText('p95')).not.toBeInTheDocument()
  })

  it('formats metric values', () => {
    expect(formatMetric(3.25, 'ms')).toBe('3.3ms')
    expect(formatMetric(846, 'ms')).toBe('846ms')
    expect(formatMetric(2394, 'ms')).toBe('2.4s')
    expect(formatMetric(15, 'count')).toBe('15.00 count')
    expect(formatMetric(0.0042, '')).toBe('0.00420')
    expect(formatMetric(1234, 'bytes')).toBe('1234 bytes')
  })
})
