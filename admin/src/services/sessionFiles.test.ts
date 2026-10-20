import { describe, it, expect, vi, beforeEach } from 'vitest'

const http = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  config: undefined as unknown,
}))

vi.mock('axios', () => ({
  default: {
    create: (config: unknown) => {
      http.config = config
      return { get: http.get, post: http.post }
    },
  },
}))

import {
  createDownloadLink,
  getSessionFileAvailability,
  getToken,
  listSessionFiles,
  sessionFilesError,
  sessionFilesLink,
  setToken,
} from './sessionFiles'

describe('session files API (APM-13)', () => {
  beforeEach(() => {
    http.get.mockReset()
    http.post.mockReset()
    window.sessionStorage.clear()
  })

  it('keeps the token in sessionStorage (this tab) only', () => {
    expect(getToken()).toBe('')
    setToken('t-1')
    expect(getToken()).toBe('t-1')
    expect(window.sessionStorage.getItem('ozx_apm_sessionfiles_token')).toBe('t-1')
    setToken('')
    expect(getToken()).toBe('')
  })

  it('survives a browser without sessionStorage', () => {
    const spy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('denied')
    })
    expect(getToken()).toBe('')
    spy.mockRestore()
  })

  it('lists with the bearer token and only the filters that are set', async () => {
    http.get.mockResolvedValue({ data: { session_files: [{ upload_id: 'a' }], count: 1 } })
    const res = await listSessionFiles({ git_commit: ' 6116e2e ', build: '', app_version: undefined, limit: 50 }, 'tok')
    expect(res.count).toBe(1)
    expect(http.get).toHaveBeenCalledWith('/session-files', {
      params: { git_commit: '6116e2e', limit: 50 },
      headers: { Authorization: 'Bearer tok' },
    })
    expect(http.config).toMatchObject({ baseURL: '/api', paramsSerializer: { indexes: null } })
  })

  it('asks for a signed download link with the token', async () => {
    http.post.mockResolvedValue({ data: { url: '/api/session-files/download?upload_id=a&expires=1&sig=x', expires_at: '', filename: 'r.zip' } })
    const link = await createDownloadLink('a', 'tok')
    expect(link.url).toContain('sig=')
    expect(http.post).toHaveBeenCalledWith('/session-files/link', { upload_id: 'a' }, { headers: { Authorization: 'Bearer tok' } })
  })

  it('asks for availability without a token, not at all for no session', async () => {
    expect(await getSessionFileAvailability([])).toEqual([])
    expect(http.get).not.toHaveBeenCalled()
    http.get.mockResolvedValue({ data: { configured: true, recordings: [{ session_id: 's1', upload_id: 'u', size_bytes: 5, created_at: '' }] } })
    const r = await getSessionFileAvailability(['s1', 's2'])
    expect(r).toHaveLength(1)
    expect(http.get).toHaveBeenCalledWith('/session-files/available', { params: { session_id: ['s1', 's2'] } })
  })

  it('explains failures', () => {
    const err = (status: number, error?: string) => ({ response: { status, data: { error } }, message: 'x' })
    expect(sessionFilesError(err(401))).toContain('token is wrong')
    expect(sessionFilesError(err(403, 'session files API disabled: no token configured'))).toContain('disabled')
    expect(sessionFilesError(err(503))).toContain('not configured')
    expect(sessionFilesError(err(410))).toContain('expired')
    expect(sessionFilesLink({ session_id: 'a b' })).toBe('/session-files?session_id=a+b')
  })
})
