import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { IssueStatusBadge, IssueStatusControl } from './IssueStatus'

const { api } = vi.hoisted(() => ({
  api: {
    getAppVersions: vi.fn(),
    setIssueState: vi.fn(),
  },
}))

vi.mock('../services/api', () => api)

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

const hostile = '<img src=x onerror=alert(1)>'

describe('IssueStatusBadge', () => {
  it('shows each state and nothing without one', () => {
    const { container, rerender } = render(<IssueStatusBadge info={{}} />)
    expect(container.textContent).toBe('')
    rerender(<IssueStatusBadge info={{ status: 'unresolved' }} />)
    expect(screen.getByText('Unresolved')).toBeInTheDocument()
    rerender(<IssueStatusBadge info={{ status: 'resolved', resolved_in_version: '1.5.0' }} />)
    expect(screen.getByText('Resolved in 1.5.0')).toBeInTheDocument()
    rerender(<IssueStatusBadge info={{ status: 'unresolved', regressed_in_version: hostile }} />)
    expect(screen.getByText(`Regressed in ${hostile}`)).toBeInTheDocument()
    expect(document.querySelector('img')).toBeNull()
  })
})

describe('IssueStatusControl', () => {
  beforeEach(() => {
    api.getAppVersions.mockReset().mockResolvedValue(['1.4.2', '1.5.0'])
    api.setIssueState.mockReset()
  })

  it('resolves with the fix version and comment', async () => {
    api.setIssueState.mockResolvedValue({ status: 'resolved' })
    render(<IssueStatusControl kind="exception" fingerprint="fp1" info={{ status: 'unresolved' }} />, { wrapper })
    fireEvent.change(screen.getByLabelText('Fixed in version'), { target: { value: ' 1.5.0 ' } })
    fireEvent.change(screen.getByLabelText('Comment'), { target: { value: 'null check' } })
    fireEvent.click(screen.getByText('Mark resolved'))
    await waitFor(() => expect(api.setIssueState).toHaveBeenCalled())
    expect(api.setIssueState.mock.calls[0][0]).toEqual({
      kind: 'exception',
      fingerprint: 'fp1',
      status: 'resolved',
      resolved_in_version: '1.5.0',
      comment: 'null check',
    })
  })

  it('resolves for the next version when no version is given', async () => {
    api.setIssueState.mockResolvedValue({ status: 'resolved' })
    render(<IssueStatusControl kind="crash" fingerprint="c1" info={{ status: 'unresolved' }} />, { wrapper })
    fireEvent.click(screen.getByText('Mark resolved'))
    await waitFor(() => expect(api.setIssueState).toHaveBeenCalled())
    expect(api.setIssueState.mock.calls[0][0]).toEqual({
      kind: 'crash',
      fingerprint: 'c1',
      status: 'resolved',
      resolved_in_version: undefined,
      comment: undefined,
    })
  })

  it('reopens a resolved issue', async () => {
    api.setIssueState.mockResolvedValue({ status: 'unresolved' })
    render(
      <IssueStatusControl
        kind="exception"
        fingerprint="fp1"
        info={{ status: 'resolved', resolved_in_version: hostile, resolved_at: '2026-10-18T10:00:00Z' }}
      />,
      { wrapper }
    )
    expect(screen.getByText(`Resolved in ${hostile}`)).toBeInTheDocument()
    expect(document.querySelector('img')).toBeNull()
    fireEvent.click(screen.getByText('Reopen'))
    await waitFor(() => expect(api.setIssueState).toHaveBeenCalled())
    expect(api.setIssueState.mock.calls[0][0]).toEqual({ kind: 'exception', fingerprint: 'fp1', status: 'unresolved' })
  })

  it('shows why a change failed, as text', async () => {
    api.setIssueState.mockRejectedValue({ response: { status: 400, data: `${hostile}\n` } })
    render(<IssueStatusControl kind="exception" fingerprint="fp1" info={{ status: 'unresolved' }} />, { wrapper })
    fireEvent.click(screen.getByText('Mark resolved'))
    expect(await screen.findByRole('alert')).toHaveTextContent(`Could not change the state: ${hostile}`)
    expect(document.querySelector('img')).toBeNull()
  })

  it('explains a 404', async () => {
    api.setIssueState.mockRejectedValue({ response: { status: 404, data: 'no exception' } })
    render(<IssueStatusControl kind="exception" fingerprint="fp1" info={{ status: 'unresolved' }} />, { wrapper })
    fireEvent.click(screen.getByText('Mark resolved'))
    expect(await screen.findByRole('alert')).toHaveTextContent('No occurrence of this group in the retained data')
  })

  it('renders nothing for an older server', () => {
    const { container } = render(<IssueStatusControl kind="exception" fingerprint="fp1" info={{}} />, { wrapper })
    expect(container.textContent).toBe('')
  })
})
