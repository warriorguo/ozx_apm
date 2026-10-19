import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import type { ReactNode } from 'react'
import { Sessions } from './Sessions'
import type { SessionTimeline } from '../types/api'

const { api } = vi.hoisted(() => ({
  api: { getSessionTimeline: vi.fn(), getDeviceSessions: vi.fn() },
}))

vi.mock('../services/api', () => api)

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

const at = (ms: number) => new Date(Date.parse('2026-10-18T10:00:00.000Z') + ms).toISOString()

const timeline: SessionTimeline = {
  session_id: 'r5',
  start_time: at(-60000),
  end_time: at(3600000),
  found: true,
  session: {
    session_id: 'r5',
    first_seen: at(0),
    last_seen: at(60000),
    device_id: 'd',
    app_version: '3.0',
    platform: 'Android',
    device_model: 'SM-A336B',
    os_version: '14',
    build_type: 'release',
    rows: 3,
  },
  items: [
    { time: at(0), type: 'startup', phase1_ms: 900, phase2_ms: 400, tti_ms: 1200 },
    {
      time: at(60000),
      type: 'run',
      run_id: 'run-<img src=x onerror=alert(1)>',
      end_reason: 'crash',
      crash_type: 'oom',
      result: 'x'.repeat(200),
      duration_ms: 600000,
      peak_rss_mb: 4800,
      peak_alloc_mb: 2400,
    },
  ],
  counts: { run: 1, startup: 1 },
  truncated: [],
  duplicate_breadcrumbs: 0,
  perf: [],
  perf_interval_seconds: 0,
}

describe('Sessions page, APM-9: runs', () => {
  it('shows a run with its end and peak memory, as text, and filters it', async () => {
    api.getSessionTimeline.mockResolvedValue(timeline)
    const { container } = renderAt('/sessions?session_id=r5')
    expect(await screen.findByText('run-<img src=x onerror=alert(1)>')).toBeInTheDocument()
    expect(container.querySelector('img')).toBeNull()
    const item = container.querySelector('li[data-type="run"]')!
    expect(item).toHaveTextContent('ended: crash (oom)')
    expect(item).toHaveTextContent('peak RSS 4800 MB native 2400 MB')
    expect(item).toHaveTextContent('600.0s')
    expect(item.textContent).toContain('x'.repeat(120) + '…')
    expect(item.textContent).not.toContain('x'.repeat(121))

    fireEvent.click(screen.getByLabelText('Show Run'))
    expect(container.querySelector('li[data-type="run"]')).toBeNull()
  })
})
