import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { FiltersProvider } from '../hooks/useFilters'
import { Health, deltaPP } from './Health'
import type { SessionHealthRow } from '../types/api'

const { api } = vi.hoisted(() => ({
  api: {
    getAppVersions: vi.fn(),
    getContextKeys: vi.fn(),
    getContextValues: vi.fn(),
    getSessionHealth: vi.fn(),
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

const row = (value: string, cfs: number, anr: number, extra: Partial<SessionHealthRow> = {}): SessionHealthRow => ({
  value,
  build_type: 'release',
  sessions: 200,
  users: 90,
  crashed_sessions: 3,
  crashed_users: 2,
  crash_free_sessions: cfs,
  crash_free_users: 0.98,
  anr_sessions: 1,
  anr_rate: anr,
  exception_sessions: 40,
  exceptions: 90,
  exception_session_rate: 0.2,
  exceptions_per_session: 0.45,
  crashes_by_type: { native: 2, oom: 1, java: 0 },
  ...extra,
})

describe('Health page (APM-9)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAppVersions.mockResolvedValue([])
    api.getContextKeys.mockResolvedValue({ keys: [{ key: 'dataVersion', count: 9 }] })
    api.getContextValues.mockResolvedValue({ key: '', values: [] })
  })

  it('shows the rates, the crashes by type and deltas against a baseline', async () => {
    api.getSessionHealth.mockResolvedValue({
      group_by: 'app_version',
      crash_types: ['native', 'java', 'oom', 'killed'],
      truncated: false,
      rows: [row('3.1', 0.9925, 0.004), row('3.0', 0.985, 0.005)],
    })
    render(<Health />, { wrapper })
    expect(await screen.findByText('99.25%')).toBeInTheDocument()
    expect(screen.getByText('98.50%')).toBeInTheDocument()
    expect(screen.getAllByText('native 2 · oom 1')).toHaveLength(2)
    expect(screen.getAllByText('0.45')).toHaveLength(2)
    expect(api.getSessionHealth).toHaveBeenCalledWith({ kind: 'by', key: 'app_version' }, ['native', 'java', 'oom', 'killed'], expect.objectContaining({ build_type: 'release' }))

    fireEvent.change(screen.getByLabelText('Compare to'), { target: { value: '3.0\u0000release' } })
    expect(await screen.findByTestId('delta-cfs-3.1')).toHaveTextContent('+0.75 pp')
    expect(screen.getByTestId('delta-cfs-3.1').className).toContain('green')
    expect(screen.getByTestId('delta-anr-3.1')).toHaveTextContent('-0.10 pp')
    expect(screen.getByTestId('delta-anr-3.1').className).toContain('green')
    expect(screen.queryByTestId('delta-cfs-3.0')).toBeNull() // the baseline itself

    fireEvent.click(screen.getByLabelText('Count Unknown'))
    await waitFor(() =>
      expect(api.getSessionHealth).toHaveBeenLastCalledWith(expect.anything(), ['native', 'java', 'oom', 'killed', 'unknown'], expect.anything())
    )
    fireEvent.click(screen.getByLabelText('Count Native'))
    await waitFor(() => expect(api.getSessionHealth).toHaveBeenLastCalledWith(expect.anything(), ['java', 'oom', 'killed', 'unknown'], expect.anything()))

    fireEvent.change(screen.getByLabelText('Group by'), { target: { value: 'key:dataVersion' } })
    await waitFor(() => expect(api.getSessionHealth).toHaveBeenLastCalledWith({ kind: 'key', key: 'dataVersion' }, expect.anything(), expect.anything()))
  })

  it('keeps at least one crash type (none would make the server count its defaults)', async () => {
    api.getSessionHealth.mockResolvedValue({ group_by: 'app_version', crash_types: [], truncated: false, rows: [row('3.1', 0.99, 0)] })
    render(<Health />, { wrapper })
    await screen.findByText('99.00%')
    for (const t of ['Native', 'Java', 'OOM', 'Killed']) {
      const box = screen.queryByLabelText(`Count ${t}`) as HTMLInputElement | null
      if (box && box.checked && !box.disabled) fireEvent.click(box)
    }
    const checked = screen.getAllByRole('checkbox').filter((b) => (b as HTMLInputElement).checked) as HTMLInputElement[]
    expect(checked).toHaveLength(1)
    expect(checked[0].disabled).toBe(true)
    await waitFor(() => expect(api.getSessionHealth).toHaveBeenLastCalledWith(expect.anything(), [expect.any(String)], expect.anything()))
  })

  it('renders game text as text and an older server row without crashes_by_type', async () => {
    const old = row('<script>alert(1)</script>', 1, 0) as Partial<SessionHealthRow>
    delete old.crashes_by_type
    api.getSessionHealth.mockResolvedValue({ group_by: '', crash_types: [], truncated: false, rows: [old] })
    const { container } = render(<Health />, { wrapper })
    expect(await screen.findByText('<script>alert(1)</script>')).toBeInTheDocument()
    expect(container.querySelector('script')).toBeNull()
    expect(screen.getByText('100.00%')).toBeInTheDocument()
  })

  it('shows empty and error states', async () => {
    api.getSessionHealth.mockResolvedValueOnce({ group_by: '', crash_types: [], truncated: false, rows: [] })
    const { unmount } = render(<Health />, { wrapper })
    expect(await screen.findByText('No sessions in this range.')).toBeInTheDocument()
    unmount()
    api.getSessionHealth.mockRejectedValue(new Error('503'))
    render(<Health />, { wrapper })
    expect(await screen.findByText('Could not load the session health.')).toBeInTheDocument()
  })

  it('formats deltas in percentage points', () => {
    expect(deltaPP(0.9925, 0.985)).toBe('+0.75 pp')
    expect(deltaPP(0.5, 0.5)).toBe('±0.00 pp')
    expect(deltaPP(0.1, 0.2)).toBe('-10.00 pp')
  })
})
