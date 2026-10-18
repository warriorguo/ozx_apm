import { describe, it, expect, vi, beforeEach } from 'vitest'

const { mockGet, mockPost } = vi.hoisted(() => ({ mockGet: vi.fn(), mockPost: vi.fn() }))

vi.mock('axios', () => ({
  default: { create: () => ({ get: mockGet, post: mockPost }) },
}))

import { getExceptions, getIssueState, setIssueState } from './api'

// APM-8: issue state.
describe('issue state API', () => {
  beforeEach(() => {
    mockGet.mockReset()
    mockPost.mockReset()
  })

  it('sends a state change as JSON with the admin header', async () => {
    mockPost.mockResolvedValue({ data: { status: 'resolved' } })
    const change = { kind: 'exception' as const, fingerprint: 'fp', status: 'resolved' as const, resolved_in_version: '1.5.0' }
    await setIssueState(change)
    expect(mockPost).toHaveBeenCalledWith('/issues/state', change, {
      headers: { 'Content-Type': 'application/json', 'X-Apm-Admin': '1' },
    })
  })

  it('reads a state by kind and fingerprint', async () => {
    mockGet.mockResolvedValue({ data: { status: 'unresolved' } })
    const st = await getIssueState('crash', 'a&b')
    expect(mockGet).toHaveBeenCalledWith('/issues/state', { params: { kind: 'crash', fingerprint: 'a&b' } })
    expect(st.status).toBe('unresolved')
  })

  it('passes the status filter of the exception list', async () => {
    mockGet.mockResolvedValue({ data: { exceptions: [], total_count: 0, page: 1, page_size: 20 } })
    await getExceptions({ status: 'resolved' })
    expect(mockGet).toHaveBeenCalledWith('/exceptions', { params: { status: 'resolved' } })
  })
})
