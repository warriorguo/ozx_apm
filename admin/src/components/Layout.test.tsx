import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { Layout } from './Layout'

const wrapper = ({ children }: { children: React.ReactNode }) => (
  <MemoryRouter>{children}</MemoryRouter>
)

describe('Layout', () => {
  it('renders header with title', () => {
    render(<Layout />, { wrapper })

    expect(screen.getByText('OZX APM')).toBeInTheDocument()
  })

  it('renders navigation links', () => {
    render(<Layout />, { wrapper })

    expect(screen.getByText('Dashboard')).toBeInTheDocument()
    expect(screen.getByText('Performance')).toBeInTheDocument()
    expect(screen.getByText('Crashes')).toBeInTheDocument()
    expect(screen.getByText('Exceptions')).toBeInTheDocument()
  })

  it('navigation links have correct hrefs', () => {
    render(<Layout />, { wrapper })

    expect(screen.getByText('Dashboard').closest('a')).toHaveAttribute('href', '/')
    expect(screen.getByText('Performance').closest('a')).toHaveAttribute('href', '/performance')
    expect(screen.getByText('Crashes').closest('a')).toHaveAttribute('href', '/crashes')
    expect(screen.getByText('Exceptions').closest('a')).toHaveAttribute('href', '/exceptions')
    // APM-9
    expect(screen.getByText('Memory').closest('a')).toHaveAttribute('href', '/memory')
    expect(screen.getByText('Health').closest('a')).toHaveAttribute('href', '/health')
    expect(screen.getByText('Funnel').closest('a')).toHaveAttribute('href', '/funnel')
  })
})
