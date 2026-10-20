import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { SessionFiles, recordedAt } from './SessionFiles'
import type { SessionFile } from '../services/sessionFiles'

const sf = vi.hoisted(() => {
  let token = ''
  return {
    getToken: vi.fn(() => token),
    setToken: vi.fn((t: string) => {
      token = t
    }),
    reset: () => {
      token = ''
    },
    listSessionFiles: vi.fn(),
    createDownloadLink: vi.fn(),
    sessionFilesError: (err: unknown) => {
      const status = (err as { response?: { status?: number } })?.response?.status
      return status === 401 ? 'The token is wrong.' : 'failed'
    },
  }
})

vi.mock('../services/sessionFiles', () => sf)

function Where() {
  const loc = useLocation()
  return <div data-testid="location">{loc.search}</div>
}

function renderAt(path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route
            path="/session-files"
            element={
              <>
                <SessionFiles />
                <Where />
              </>
            }
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const file: SessionFile = {
  upload_id: '13131313131313131313131313131313',
  status: 'complete',
  created_at: '2026-10-20T16:00:00Z',
  updated_at: '2026-10-20T16:00:05Z',
  expires_at: '2026-11-19T16:00:00Z',
  device_id: 'a13a13a13a13a13a13a13a13a13a13a1',
  session_id: '1761000000000_a10a10a1',
  recording_name: '2026-10-20_153042',
  app_version: '1.4.0',
  app_build: '140',
  build_type: 'dev',
  platform: 'Android',
  device_model: 'samsung SM-A336B',
  os_version: 'Android OS 16',
  unity_version: '2022.3.62f3',
  sdk_version: '2.1.0',
  git_commit: '6116e2e4ff38de10eac61ba3c425e8f50a3aacdb',
  started_at: '2026-10-20T15:30:42.1230000Z',
  scene: 'Gameplay',
  data_source: 'builtin',
  data_pack_version: '0',
  data_pack_sha256: '',
  size_bytes: 5 * 1024 * 1024,
  part_bytes: 524288,
  part_count: 10,
  sha256: 'ab'.repeat(32),
  file_count: 12,
  raw_bytes: 50 * 1024 * 1024,
}

describe('Session files page (APM-13)', () => {
  const assign = vi.fn()
  const original = window.location

  beforeEach(() => {
    sf.reset()
    sf.listSessionFiles.mockReset()
    sf.createDownloadLink.mockReset()
    sf.setToken.mockClear()
    assign.mockReset()
    Object.defineProperty(window, 'location', { configurable: true, value: { ...original, assign } })
  })

  afterEach(() => {
    Object.defineProperty(window, 'location', { configurable: true, value: original })
  })

  it('asks for the token first and lists nothing without it', () => {
    renderAt('/session-files')
    expect(screen.getByPlaceholderText('OZX_ADMIN_SESSIONFILES_TOKEN')).toBeInTheDocument()
    expect(sf.listSessionFiles).not.toHaveBeenCalled()
  })

  it('lists with the token, filters by commit, downloads through a signed link', async () => {
    sf.listSessionFiles.mockResolvedValue({ session_files: [file], count: 1 })
    sf.createDownloadLink.mockResolvedValue({ url: '/api/session-files/download?upload_id=x&expires=1&sig=s', expires_at: '', filename: 'r.zip' })
    renderAt('/session-files?git_commit=6116e2e')
    fireEvent.change(screen.getByPlaceholderText('OZX_ADMIN_SESSIONFILES_TOKEN'), { target: { value: ' secret ' } })
    fireEvent.click(screen.getByText('Use for this tab'))
    expect(sf.setToken).toHaveBeenCalledWith('secret')
    await screen.findByText('2026-10-20_153042')
    expect(sf.listSessionFiles).toHaveBeenCalledWith({ limit: 200, git_commit: '6116e2e' }, 'secret')
    expect(screen.getByText('6116e2e4ff')).toBeInTheDocument()
    expect(screen.getByText('5.0 MB')).toBeInTheDocument()
    expect(screen.getByText('Session time line').closest('a')?.getAttribute('href')).toContain('session_id=1761000000000_a10a10a1')
    fireEvent.click(screen.getByText('Download'))
    await waitFor(() => expect(assign).toHaveBeenCalledWith('/api/session-files/download?upload_id=x&expires=1&sig=s'))
    expect(sf.createDownloadLink).toHaveBeenCalledWith(file.upload_id, 'secret')
  })

  it('a wrong token asks again', async () => {
    sf.setToken('wrong')
    sf.listSessionFiles.mockRejectedValue({ response: { status: 401 } })
    renderAt('/session-files')
    await screen.findByText('The token is wrong.')
    expect(screen.getByPlaceholderText('OZX_ADMIN_SESSIONFILES_TOKEN')).toBeInTheDocument()
  })

  it('the filter form goes to the URL', async () => {
    sf.setToken('t')
    sf.listSessionFiles.mockResolvedValue({ session_files: [], count: 0 })
    renderAt('/session-files')
    await screen.findByText('No session files match.')
    fireEvent.change(screen.getByPlaceholderText('prefix, e.g. 6116e2e'), { target: { value: 'abc1234' } })
    fireEvent.click(screen.getByText('Search'))
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('?git_commit=abc1234'))
    await waitFor(() => expect(sf.listSessionFiles).toHaveBeenLastCalledWith({ limit: 200, git_commit: 'abc1234' }, 't'))
  })

  it('places the recording by session.json startedAt', () => {
    expect(recordedAt({ started_at: '2026-10-20T15:30:42.1230000Z', created_at: 'x' })).toBe('2026-10-20T15:30:42.123Z')
    expect(recordedAt({ started_at: '', created_at: '2026-10-20T16:00:00Z' })).toBe('2026-10-20T16:00:00Z')
    expect(recordedAt({ started_at: 'garbage', created_at: 'c' })).toBe('c')
  })
})
