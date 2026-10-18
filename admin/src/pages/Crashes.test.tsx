import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { FiltersProvider } from '../hooks/useFilters'
import { Crashes } from './Crashes'
import { CrashDetail } from './CrashDetail'

const { api } = vi.hoisted(() => ({
  api: {
    getAppVersions: vi.fn(),
    getContextKeys: vi.fn(),
    getContextValues: vi.fn(),
    getCrashes: vi.fn(),
    getCrashDetail: vi.fn(),
    setIssueState: vi.fn(),
  },
}))

vi.mock('../services/api', () => api)

function renderAt(path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <FiltersProvider>
          <Routes>
            <Route path="/crashes" element={<Crashes />} />
            <Route path="/crashes/detail" element={<CrashDetail />} />
          </Routes>
        </FiltersProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const crash = {
  fingerprint: 'crash-fp',
  crash_type: 'native',
  sample_message: '',
  count: 12,
  session_count: 12,
  first_seen: '2026-10-18T10:00:00Z',
  last_seen: '2026-10-18T11:00:00Z',
  affected_versions: ['3.0.0'],
  top_devices: [],
  status: 'unresolved',
}

const detail = {
  ...crash,
  stack: 'SIGSEGV\n#00 pc 0001 libgame.so',
  occurrences: [],
  version_distribution: [],
  device_distribution: [],
  os_distribution: [],
  sample: null,
  symbols: [],
}

// APM-8: crash group state, and the alert's link /crashes?fingerprint=F,
// which opens the group's detail page (APM-7).
describe('Crashes page (APM-8)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAppVersions.mockResolvedValue(['3.0.0'])
    api.getContextKeys.mockResolvedValue({ keys: [] })
    api.getContextValues.mockResolvedValue({ key: '', values: [] })
    api.getCrashes.mockResolvedValue({ crashes: [crash], total_count: 1, page: 1, page_size: 20 })
    api.getCrashDetail.mockResolvedValue(detail)
  })

  it('opens the group of the link and resolves it', async () => {
    api.setIssueState.mockResolvedValue({ status: 'resolved' })
    renderAt('/crashes?fingerprint=crash-fp')
    expect(await screen.findByText('Mark resolved')).toBeInTheDocument()
    expect(api.getCrashDetail).toHaveBeenCalledWith('crash-fp', expect.anything())
    expect(screen.getAllByText('Unresolved').length).toBeGreaterThan(0)
    fireEvent.change(screen.getByLabelText('Fixed in version'), { target: { value: '3.0.1' } })
    fireEvent.click(screen.getByText('Mark resolved'))
    await waitFor(() =>
      expect(api.setIssueState.mock.calls[0]?.[0]).toEqual(
        expect.objectContaining({ kind: 'crash', fingerprint: 'crash-fp', status: 'resolved', resolved_in_version: '3.0.1' })
      )
    )
    // The detail is reloaded.
    await waitFor(() => expect(api.getCrashDetail.mock.calls.length).toBeGreaterThan(1))
  })

  it('lists the groups with their state without a fingerprint', async () => {
    renderAt('/crashes')
    expect(await screen.findByText('Unresolved')).toBeInTheDocument()
    expect(api.getCrashDetail).not.toHaveBeenCalled()
  })
})
