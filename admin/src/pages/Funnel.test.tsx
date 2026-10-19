import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { FiltersProvider } from '../hooks/useFilters'
import { Funnel, buildSteps } from './Funnel'

const { api } = vi.hoisted(() => ({
  api: {
    getAppVersions: vi.fn(),
    getContextKeys: vi.fn(),
    getContextValues: vi.fn(),
    getCustomEvents: vi.fn(),
    getFunnel: vi.fn(),
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

const result = (value: string, sessions: number[]) => ({
  value,
  steps: sessions.map((n, i) => ({
    name: ['run_start', 'room_enter', 'run_end'][i],
    props: i === 1 ? { roomType: 'boss' } : undefined,
    sessions: n,
    conversion: n / sessions[0],
    from_previous: i === 0 ? 1 : n / sessions[i - 1],
  })),
})

describe('Funnel page (APM-9)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAppVersions.mockResolvedValue([])
    api.getContextKeys.mockResolvedValue({ keys: [] })
    api.getContextValues.mockResolvedValue({ key: '', values: [] })
    api.getCustomEvents.mockResolvedValue({ events: [{ name: 'run_start', count: 9, sessions: 9, devices: 9, last_seen: '' }], truncated: false })
  })

  it('builds the steps from the editor and shows the conversion per version', async () => {
    api.getFunnel.mockResolvedValue({
      start_time: '',
      end_time: '',
      window_seconds: 1800,
      group_by: 'app_version',
      steps: [],
      truncated: false,
      groups: [result('3.0', [4, 2, 1]), result('<img src=x onerror=alert(1)>', [1, 1, 1])],
    })
    const { container } = render(<Funnel />, { wrapper })
    expect(screen.getByText('Define the steps and run the funnel.')).toBeInTheDocument()
    expect(api.getFunnel).not.toHaveBeenCalled()
    await waitFor(() => expect(container.querySelector('datalist option[value="run_start"]')).not.toBeNull())

    fireEvent.change(screen.getByLabelText('Step 1 event'), { target: { value: 'run_start' } })
    fireEvent.change(screen.getByLabelText('Step 2 event'), { target: { value: 'room_enter' } })
    fireEvent.click(screen.getAllByText('+ property')[1])
    fireEvent.change(screen.getByLabelText('Step 2 property 1 key'), { target: { value: 'roomType' } })
    fireEvent.change(screen.getByLabelText('Step 2 property 1 value'), { target: { value: 'boss' } })
    fireEvent.click(screen.getByText('+ step'))
    fireEvent.change(screen.getByLabelText('Step 3 event'), { target: { value: 'run_end' } })
    fireEvent.change(screen.getByLabelText('Window'), { target: { value: '30' } })
    fireEvent.change(screen.getByLabelText('Window unit'), { target: { value: 'm' } })
    fireEvent.change(screen.getByLabelText('Split by'), { target: { value: 'app_version' } })
    fireEvent.click(screen.getByText('Run'))

    await waitFor(() =>
      expect(api.getFunnel).toHaveBeenCalledWith(
        [{ name: 'run_start' }, { name: 'room_enter', props: { roomType: 'boss' } }, { name: 'run_end' }],
        1800,
        'app_version',
        expect.objectContaining({ build_type: 'release' })
      )
    )
    const groups = await screen.findAllByTestId('funnel-group')
    expect(groups).toHaveLength(2)
    expect(groups[0]).toHaveTextContent('3.0')
    expect(groups[0]).toHaveTextContent('25.00%') // run_end from the first step
    expect(groups[0]).toHaveTextContent('(roomType=boss)')
    expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeInTheDocument()
    expect(container.querySelector('img')).toBeNull()
  })

  it('refuses invalid names and keys, and moves and removes steps', async () => {
    render(<Funnel />, { wrapper })
    fireEvent.change(screen.getByLabelText('Step 1 event'), { target: { value: 'run start' } })
    fireEvent.change(screen.getByLabelText('Step 2 event'), { target: { value: 'b' } })
    fireEvent.click(screen.getByText('Run'))
    expect(await screen.findByText(/Step 1: the event name must be/)).toBeInTheDocument()
    expect(api.getFunnel).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText('Step 1 event'), { target: { value: 'a' } })
    fireEvent.click(screen.getByLabelText('Move step 1 down'))
    expect((screen.getByLabelText('Step 1 event') as HTMLInputElement).value).toBe('b')
    fireEvent.click(screen.getByLabelText('Remove step 2'))
    expect(screen.getAllByTestId('funnel-step')).toHaveLength(1)

    fireEvent.change(screen.getByLabelText('Window'), { target: { value: '25' } })
    fireEvent.click(screen.getByText('Run'))
    expect(await screen.findByText(/between 1 minute and 24 hours/)).toBeInTheDocument()
    expect(api.getFunnel).not.toHaveBeenCalled()
  })

  it('shows errors and an empty result', async () => {
    api.getFunnel.mockResolvedValueOnce({ start_time: '', end_time: '', window_seconds: 7200, group_by: '', steps: [], truncated: false, groups: [] })
    render(<Funnel />, { wrapper })
    fireEvent.change(screen.getByLabelText('Step 1 event'), { target: { value: 'a' } })
    fireEvent.click(screen.getByLabelText('Remove step 2'))
    fireEvent.click(screen.getByText('Run'))
    expect(await screen.findByText('No session reached the first step.')).toBeInTheDocument()
    expect(api.getFunnel).toHaveBeenCalledWith([{ name: 'a' }], 7200, '', expect.anything())

    api.getFunnel.mockRejectedValue(new Error('400'))
    fireEvent.change(screen.getByLabelText('Window'), { target: { value: '3' } })
    fireEvent.click(screen.getByText('Run'))
    expect(await screen.findByText('Could not compute the funnel.')).toBeInTheDocument()
  })

  it('validates the steps', () => {
    expect(buildSteps([]).error).toMatch(/at least one/)
    expect(buildSteps(Array.from({ length: 9 }, () => ({ name: 'a', props: [] }))).error).toMatch(/At most 8/)
    expect(buildSteps([{ name: 'a', props: [{ key: 'bad key', value: 'v' }] }]).error).toMatch(/property keys/)
    expect(buildSteps([{ name: 'a', props: [{ key: 'k', value: 'v'.repeat(257) }] }]).error).toMatch(/256/)
    // An empty property row is ignored; names are trimmed.
    expect(buildSteps([{ name: ' a ', props: [{ key: '', value: '' }] }])).toEqual({ steps: [{ name: 'a' }], error: '' })
  })
})
