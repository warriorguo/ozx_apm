import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { FilterBar } from './FilterBar'

const { api } = vi.hoisted(() => ({
  api: {
    getAppVersions: vi.fn(),
    getContextKeys: vi.fn(),
    getContextValues: vi.fn(),
  },
}))

vi.mock('../services/api', () => api)

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

const base = {
  timePreset: '24h' as const,
  appVersion: '',
  platform: '',
  formattedRange: 'range',
  onTimePresetChange: vi.fn(),
  onAppVersionChange: vi.fn(),
  onPlatformChange: vi.fn(),
}

// APM-5: build type selector and context filters.
describe('FilterBar build type and context', () => {
  beforeEach(() => {
    api.getAppVersions.mockResolvedValue([])
    api.getContextKeys.mockResolvedValue({ keys: [{ key: 'levelId', count: 3 }] })
    api.getContextValues.mockResolvedValue({ key: 'levelId', values: [{ value: '1', count: 3 }] })
  })

  it('hides the new controls unless their handlers are given', () => {
    render(<FilterBar {...base} />, { wrapper })
    expect(screen.queryByLabelText('Build type')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Context key')).not.toBeInTheDocument()
  })

  it('selects a build type, release by default', () => {
    const onBuildTypeChange = vi.fn()
    render(<FilterBar {...base} onBuildTypeChange={onBuildTypeChange} />, { wrapper })
    const select = screen.getByLabelText('Build type') as HTMLSelectElement
    expect(select.value).toBe('release')
    fireEvent.change(select, { target: { value: 'editor' } })
    expect(onBuildTypeChange).toHaveBeenCalledWith('editor')
  })

  it('adds and removes context filters, rejecting invalid keys', () => {
    const onAdd = vi.fn()
    const onRemove = vi.fn()
    render(
      <FilterBar
        {...base}
        contextFilters={[{ key: 'roomId', value: '' }]}
        onAddContextFilter={onAdd}
        onRemoveContextFilter={onRemove}
      />,
      { wrapper }
    )
    expect(screen.getByText('roomId = (not set)')).toBeInTheDocument()
    fireEvent.click(screen.getByLabelText('Remove roomId filter'))
    expect(onRemove).toHaveBeenCalledWith(0)

    const add = screen.getByText('Add filter')
    fireEvent.change(screen.getByLabelText('Context key'), { target: { value: 'bad key' } })
    expect(add).toBeDisabled()

    fireEvent.change(screen.getByLabelText('Context key'), { target: { value: 'levelId' } })
    fireEvent.change(screen.getByLabelText('Context value'), { target: { value: '3' } })
    expect(add).not.toBeDisabled()
    fireEvent.click(add)
    expect(onAdd).toHaveBeenCalledWith({ key: 'levelId', value: '3' })
  })
})
