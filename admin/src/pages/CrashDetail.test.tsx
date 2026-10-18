import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import type { ReactNode } from 'react'
import { FiltersProvider } from '../hooks/useFilters'
import { CrashDetail, frameLabel } from './CrashDetail'
import { Crashes } from './Crashes'
import { normalizeCrashOccurrence } from '../services/api'
import type { CrashDetail as Detail } from '../types/api'

const { api } = vi.hoisted(() => ({
  api: {
    getAppVersions: vi.fn(),
    getContextKeys: vi.fn(),
    getContextValues: vi.fn(),
    getCrashes: vi.fn(),
    getCrashDetail: vi.fn(),
  },
}))

vi.mock('../services/api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../services/api')>()
  return { ...api, normalizeCrashOccurrence: real.normalizeCrashOccurrence }
})

function Where() {
  const loc = useLocation()
  return <div data-testid="where">{loc.pathname + loc.search}</div>
}

function renderAt(path: string, page: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <FiltersProvider>
          <Routes>
            <Route path="/crashes" element={page} />
            <Route path="/crashes/detail" element={page} />
          </Routes>
          <Where />
        </FiltersProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const buildId = '0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077'

const sample = normalizeCrashOccurrence({
  timestamp: '2026-10-18T10:00:34.000Z',
  event_id: 'c1',
  app_version: '2.1.0',
  platform: 'Android',
  device_model: 'SM-A336B',
  os_version: 'Android OS 14 / API-34',
  scene: 'Dungeon',
  build_type: 'release',
  session_id: '1760780000000_ab12&x=1',
  device_id: 'dev 1',
  crash_type: 'native',
  summary: 'SIGSEGV (SEGV_MAPERR) at 0x0000000000000008 in ApmTestDriver.DeepC (libil2cpp.so)',
  exit_reason: 'CRASH_NATIVE',
  exit_status: 11,
  exit_description: 'crash',
  importance: 100,
  pss_kb: 512000,
  rss_kb: 600000,
  signal: 'SIGSEGV',
  signal_code: 'SEGV_MAPERR',
  fault_addr: '0x0000000000000008',
  abort_message: '',
  abi: 'arm64-v8a',
  session_ms: 34000,
  foreground: true,
  details: { detected_by: 'exit_info', fps: '58.9' },
  context: { roomId: 'r-12', note: '<script>alert(1)</script>' },
  breadcrumbs: ['[10:00:01.000] Event: room_enter roomId=r-12', '[10:00:01.200] Warning: <img src=x onerror=alert(1)>'],
  stack: 'Thread "UnityMain" tid=4242 crashed\n#00 pc 00000000005417bc libil2cpp.so (BuildId: ' + buildId + ')\n',
  threads: [
    {
      name: 'UnityMain',
      tid: 4242,
      crashed: true,
      omitted: 0,
      frames: [
        { index: 0, native: true, pc: '0x5417bc', module: 'libil2cpp.so', build_id: buildId, function: 'ApmTestDriver.DeepC(System.Int32)', offset: 32, file: 'Assets/Scripts/ApmTestDriver.cs', line: 812, in_app: true, symbolicated: true, symbols_by: 'build_id', raw: '#00 pc ...' },
        { index: 1, native: true, pc: '0xa1b2c4', module: 'libunity.so', build_id: '1111', in_app: true, symbolicated: false, raw: '#01 pc ...' },
        { index: 2, native: true, pc: '0xc8d2c', module: '/apex/com.android.runtime/lib64/bionic/libc.so', function: '__pthread_start(void*)', offset: 208, in_app: false, symbolicated: false, raw: '#02 pc ...' },
      ],
    },
  ],
})

const detail: Detail = {
  fingerprint: 'abc123',
  crash_type: 'native',
  stack: sample.stack!,
  count: 3,
  session_count: 2,
  first_seen: '2026-10-17T10:00:00Z',
  last_seen: '2026-10-18T10:00:34Z',
  occurrences: [{ ...sample, stack: '', threads: [] }],
  version_distribution: [{ version: '2.1.0', count: 3 }],
  device_distribution: [{ device: 'SM-A336B', count: 3 }],
  os_distribution: [],
  sample,
  fingerprint_version: 1,
  symbols: [
    { module: 'libil2cpp.so', build_id: buildId, found: true, upload_id: 'u1', uploaded_at: '2026-10-18T09:00:00Z', line_map: true, method_map: true },
    { module: 'libunity.so', build_id: '1111', found: false, line_map: false, method_map: false },
  ],
}

describe('Crash pages (APM-7)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAppVersions.mockResolvedValue([])
    api.getContextKeys.mockResolvedValue({ keys: [] })
    api.getContextValues.mockResolvedValue({ key: '', values: [] })
  })

  it('shows the exit, the symbolicated stack with file and line, the context and breadcrumbs, as text', async () => {
    api.getCrashDetail.mockResolvedValue(detail)
    renderAt('/crashes/detail?fingerprint=abc123', <CrashDetail />)

    const top = await screen.findByText('ApmTestDriver.DeepC(System.Int32)')
    expect(top.closest('li')).toHaveTextContent('ApmTestDriver.DeepC(System.Int32) (Assets/Scripts/ApmTestDriver.cs:812) · libil2cpp.so 0x5417bc')
    expect(screen.getByText('libunity.so + 0xa1b2c4')).toBeInTheDocument()
    expect(screen.getByText('__pthread_start(void*)')).toBeInTheDocument()
    expect(screen.getAllByText(sample.summary!).length).toBe(2) // the summary and the occurrence
    expect(screen.getByText('CRASH_NATIVE · status 11')).toBeInTheDocument()
    expect(screen.getByText('SIGSEGV (SEGV_MAPERR) · fault addr 0x0000000000000008')).toBeInTheDocument()
    expect(screen.getByText('PSS 500 MB · RSS 586 MB')).toBeInTheDocument()
    expect(api.getCrashDetail).toHaveBeenCalledWith('abc123', expect.objectContaining({ build_type: 'release' }))

    const status = screen.getByTestId('symbol-status')
    expect(within(status).getByText(/symbols uploaded .*with C# lines/)).toBeInTheDocument()
    expect(within(status).getByText('no symbols uploaded')).toBeInTheDocument()

    // Game text is text.
    expect(screen.getByText('<script>alert(1)</script>')).toBeInTheDocument()
    expect(screen.getAllByText('<img src=x onerror=alert(1)>').length).toBeGreaterThan(0)
    expect(document.querySelector('img')).toBeNull()
    expect(document.querySelector('script')).toBeNull()

    // Links to the session time line at the crash and to the device's sessions are encoded.
    const link = screen.getAllByText('Session time line at the crash')[0].closest('a')!
    const href = new URL(link.getAttribute('href')!, 'http://x')
    expect(href.pathname).toBe('/sessions')
    expect(href.searchParams.get('session_id')).toBe('1760780000000_ab12&x=1')
    expect(href.searchParams.get('at')).toBe('2026-10-18T10:00:34.000Z')
    const dev = new URL(screen.getAllByText('Its sessions')[0].closest('a')!.getAttribute('href')!, 'http://x')
    expect(dev.searchParams.get('device_id')).toBe('dev 1')
    expect(screen.getAllByTestId('crash-occurrence')).toHaveLength(1)
  })

  it('works with an older server (no sample, no symbols) and a kill without stack', async () => {
    api.getCrashDetail.mockResolvedValue({
      ...detail,
      crash_type: 'oom',
      stack: '',
      sample: null,
      symbols: [],
      occurrences: [normalizeCrashOccurrence({ timestamp: '2026-10-18T10:00:00Z', app_version: '1', platform: 'Android', device_model: 'x', os_version: '13', scene: '', breadcrumbs: [] } as never)],
    })
    renderAt('/crashes/detail?fingerprint=abc123', <CrashDetail />)
    expect(await screen.findByText(/No stack: the process was killed/)).toBeInTheDocument()
    expect(screen.getByText('Killed for low memory')).toBeInTheDocument()
  })

  it('says when nothing matches the filters', async () => {
    api.getCrashDetail.mockRejectedValue({ response: { status: 404 } })
    renderAt('/crashes/detail?fingerprint=abc123', <CrashDetail />)
    expect(await screen.findByText(/No crash of this group matches the current filters/)).toBeInTheDocument()
  })

  it('lists crash groups with the symbolicated top frame; a row opens the detail', async () => {
    api.getCrashes.mockResolvedValue({
      crashes: [
        {
          fingerprint: 'a b&c',
          crash_type: 'native',
          sample_message: sample.summary,
          count: 3,
          session_count: 2,
          first_seen: '2026-10-17T10:00:00Z',
          last_seen: '2026-10-18T10:00:34Z',
          affected_versions: ['2.1.0'],
          top_devices: ['SM-A336B'],
          exit_reason: 'CRASH_NATIVE',
          signal: 'SIGSEGV',
          crash_site: 'libil2cpp.so!ApmTestDriver.DeepC',
          top_frame: 'ApmTestDriver.DeepC(System.Int32) (Assets/Scripts/ApmTestDriver.cs:812)',
          foreground: 3,
          symbolicated: true,
        },
      ],
      total_count: 1,
      page: 1,
      page_size: 20,
    })
    renderAt('/crashes', <Crashes />)
    const row = await screen.findByText('ApmTestDriver.DeepC(System.Int32) (Assets/Scripts/ApmTestDriver.cs:812)')
    expect(screen.getByText(sample.summary!)).toBeInTheDocument()
    fireEvent.click(row)
    const where = screen.getByTestId('where').textContent!
    const url = new URL(where, 'http://x')
    expect(url.pathname).toBe('/crashes/detail')
    expect(url.searchParams.get('fingerprint')).toBe('a b&c')
  })

  it('labels frames without a function by library and address', () => {
    expect(frameLabel({ index: 0, native: true, pc: '0x10', module: 'libil2cpp.so', in_app: true, symbolicated: false, raw: 'r' })).toBe('libil2cpp.so + 0x10')
    expect(frameLabel({ index: 0, native: false, function: 'com.x.Y.z', in_app: true, symbolicated: false, raw: 'at com.x.Y.z()' })).toBe('com.x.Y.z')
  })
})
