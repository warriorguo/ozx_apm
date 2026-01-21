import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { StatCard } from './StatCard'

describe('StatCard', () => {
  it('renders title and value', () => {
    render(<StatCard title="Total Sessions" value={1000} />)

    expect(screen.getByText('Total Sessions')).toBeInTheDocument()
    expect(screen.getByText('1000')).toBeInTheDocument()
  })

  it('renders subtitle when provided', () => {
    render(<StatCard title="Crash Rate" value="0.5%" subtitle="50 crashes" />)

    expect(screen.getByText('50 crashes')).toBeInTheDocument()
  })

  it('renders trend when provided', () => {
    render(
      <StatCard
        title="Crashes"
        value={100}
        trend="up"
        trendValue="10%"
      />
    )

    expect(screen.getByText('+10%')).toBeInTheDocument()
    expect(screen.getByText('vs previous')).toBeInTheDocument()
  })

  it('renders down trend correctly', () => {
    render(
      <StatCard
        title="Crashes"
        value={100}
        trend="down"
        trendValue="5%"
      />
    )

    expect(screen.getByText('-5%')).toBeInTheDocument()
  })

  it('applies color classes correctly', () => {
    const { container, rerender } = render(
      <StatCard title="Test" value={100} color="success" />
    )

    expect(container.querySelector('.bg-green-500')).toBeInTheDocument()

    rerender(<StatCard title="Test" value={100} color="danger" />)
    expect(container.querySelector('.bg-red-500')).toBeInTheDocument()

    rerender(<StatCard title="Test" value={100} color="warning" />)
    expect(container.querySelector('.bg-yellow-500')).toBeInTheDocument()
  })

  it('uses default color when not specified', () => {
    const { container } = render(<StatCard title="Test" value={100} />)

    expect(container.querySelector('.bg-blue-500')).toBeInTheDocument()
  })
})
