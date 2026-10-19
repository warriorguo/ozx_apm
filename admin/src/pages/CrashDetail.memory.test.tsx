import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { FiltersProvider } from '../hooks/useFilters'
import { CrashDetail, detailNumber } from './CrashDetail'
import { normalizeCrashOccurrence } from '../services/api'
import type { CrashDetail as Detail, CrashOccurrence } from '../types/api'

const { api } = vi.hoisted(() => ({
  api: {
    getAppVersions: vi.fn(),
    getContextKeys: vi.fn(),
    getContextValues: vi.fn(),
    getCrashDetail: vi.fn(),
  },
}))

vi.mock('../services/api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../services/api')>()
  return { ...api, normalizeCrashOccurrence: real.normalizeCrashOccurrence }
})

function renderDetail() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/crashes/detail?fingerprint=oom1']}>
        <FiltersProvider>
          <Routes>
            <Route path="/crashes/detail" element={<CrashDetail />} />
          </Routes>
        </FiltersProvider>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

function detailWith(details: Record<string, string>, extra: Partial<CrashOccurrence> = {}): Detail {
  const sample = normalizeCrashOccurrence({
    timestamp: '2026-10-18T10:00:34.000Z',
    app_version: '3.0',
    platform: 'Android',
    device_model: 'SM-A336B',
    os_version: '14',
    scene: 'Dungeon',
    breadcrumbs: [],
    crash_type: 'oom',
    summary: 'Killed for low memory in the foreground (PSS 4.5 GB)',
    exit_reason: 'LOW_MEMORY',
    pss_kb: 4718592,
    rss_kb: 5033164,
    foreground: true,
    details,
    ...extra,
  })
  return {
    fingerprint: 'oom1',
    crash_type: 'oom',
    stack: '',
    count: 1,
    session_count: 1,
    first_seen: '2026-10-18T10:00:34Z',
    last_seen: '2026-10-18T10:00:34Z',
    occurrences: [],
    version_distribution: [],
    device_distribution: [],
    os_distribution: [],
    sample,
    fingerprint_version: 1,
    symbols: [],
  }
}

describe('Crash detail, APM-9: memory before death', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAppVersions.mockResolvedValue([])
    api.getContextKeys.mockResolvedValue({ keys: [] })
    api.getContextValues.mockResolvedValue({ key: '', values: [] })
  })

  it('shows the last sample, the run and process peaks and Android at the exit', async () => {
    api.getCrashDetail.mockResolvedValue(
      detailWith({
        detected_by: 'exit_info',
        mem_rss_mb: '4600.5',
        mem_alloc_mb: '2100',
        mem_mb: '180',
        mem_age_ms: '800',
        run_id: 'run-<b>42</b>',
        run_ms: '754000',
        run_peak_rss_mb: '4700',
        run_peak_alloc_mb: '2150',
        proc_peak_rss_mb: '4720',
        mem_gfx_mb: 'lots', // not a number: not shown
      })
    )
    renderDetail()
    const section = await screen.findByTestId('memory-before-death')
    expect(section).toHaveTextContent('Memory before death')
    expect(section).toHaveTextContent('Last sample (800ms before)')
    const rss = within(section).getByText('Resident (RSS)').closest('tr')!
    expect(rss).toHaveTextContent('4.49 GB')
    expect(rss).toHaveTextContent('4.59 GB')
    expect(within(section).getByText('Native allocated').closest('tr')).toHaveTextContent('2.05 GB2.10 GB')
    expect(within(section).queryByText('Graphics')).toBeNull()
    expect(section).toHaveTextContent('Peak resident memory of the process: 4.61 GB')
    expect(section).toHaveTextContent('Android at the exit: PSS ')
    expect(within(section).getByText('run-<b>42</b>')).toBeInTheDocument()
    expect(section.querySelector('b')).toBeNull()
    expect(section).toHaveTextContent('12m 34s in')
  })

  it('shows nothing for a crash of an older SDK', async () => {
    api.getCrashDetail.mockResolvedValue(detailWith({ detected_by: 'exit_info', fps: '58.9' }))
    renderDetail()
    expect(await screen.findByText('How the process ended')).toBeInTheDocument()
    expect(screen.queryByTestId('memory-before-death')).toBeNull()
  })

  it('parses numbers defensively', () => {
    expect(detailNumber({ a: '12.5' }, 'a')).toBe(12.5)
    expect(detailNumber({ a: 'x' }, 'a')).toBeUndefined()
    expect(detailNumber({ a: '' }, 'a')).toBeUndefined()
    expect(detailNumber({ a: 'Infinity' }, 'a')).toBeUndefined()
    expect(detailNumber({ a: '1'.repeat(40) }, 'a')).toBeUndefined()
    expect(detailNumber(undefined, 'a')).toBeUndefined()
  })
})
