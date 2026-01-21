import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { FilterBar } from './FilterBar'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: false },
  },
})

const wrapper = ({ children }: { children: React.ReactNode }) => (
  <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
)

describe('FilterBar', () => {
  const defaultProps = {
    timePreset: '24h' as const,
    appVersion: '',
    platform: '',
    formattedRange: 'Jan 1, 00:00 - Jan 2, 00:00',
    onTimePresetChange: vi.fn(),
    onAppVersionChange: vi.fn(),
    onPlatformChange: vi.fn(),
  }

  it('renders time preset buttons', () => {
    render(<FilterBar {...defaultProps} />, { wrapper })

    expect(screen.getByText('1 Hour')).toBeInTheDocument()
    expect(screen.getByText('6 Hours')).toBeInTheDocument()
    expect(screen.getByText('24 Hours')).toBeInTheDocument()
    expect(screen.getByText('7 Days')).toBeInTheDocument()
    expect(screen.getByText('30 Days')).toBeInTheDocument()
  })

  it('highlights selected time preset', () => {
    render(<FilterBar {...defaultProps} timePreset="7d" />, { wrapper })

    const button = screen.getByText('7 Days')
    expect(button).toHaveClass('bg-blue-500')
  })

  it('calls onTimePresetChange when clicking preset', () => {
    const onTimePresetChange = vi.fn()
    render(<FilterBar {...defaultProps} onTimePresetChange={onTimePresetChange} />, { wrapper })

    fireEvent.click(screen.getByText('7 Days'))
    expect(onTimePresetChange).toHaveBeenCalledWith('7d')
  })

  it('renders platform select', () => {
    render(<FilterBar {...defaultProps} />, { wrapper })

    expect(screen.getByText('All Platforms')).toBeInTheDocument()
  })

  it('calls onPlatformChange when selecting platform', () => {
    const onPlatformChange = vi.fn()
    render(<FilterBar {...defaultProps} onPlatformChange={onPlatformChange} />, { wrapper })

    fireEvent.change(screen.getAllByRole('combobox')[1], { target: { value: 'Android' } })
    expect(onPlatformChange).toHaveBeenCalledWith('Android')
  })

  it('displays formatted time range', () => {
    render(<FilterBar {...defaultProps} formattedRange="Jan 1, 10:00 - Jan 2, 10:00" />, { wrapper })

    expect(screen.getByText('Jan 1, 10:00 - Jan 2, 10:00')).toBeInTheDocument()
  })
})
