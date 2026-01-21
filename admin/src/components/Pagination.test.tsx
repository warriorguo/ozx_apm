import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { Pagination } from './Pagination'

describe('Pagination', () => {
  it('renders nothing when total pages <= 1', () => {
    const { container } = render(
      <Pagination page={1} pageSize={20} totalCount={10} onPageChange={() => {}} />
    )

    expect(container.firstChild).toBeNull()
  })

  it('renders pagination when total pages > 1', () => {
    render(
      <Pagination page={1} pageSize={20} totalCount={100} onPageChange={() => {}} />
    )

    expect(screen.getByText('Previous')).toBeInTheDocument()
    expect(screen.getByText('Next')).toBeInTheDocument()
  })

  it('shows correct result count', () => {
    render(
      <Pagination page={1} pageSize={20} totalCount={100} onPageChange={() => {}} />
    )

    expect(screen.getByText(/Showing 1 to 20 of 100 results/)).toBeInTheDocument()
  })

  it('shows correct result count for middle page', () => {
    render(
      <Pagination page={2} pageSize={20} totalCount={100} onPageChange={() => {}} />
    )

    expect(screen.getByText(/Showing 21 to 40 of 100 results/)).toBeInTheDocument()
  })

  it('shows correct result count for last page', () => {
    render(
      <Pagination page={5} pageSize={20} totalCount={95} onPageChange={() => {}} />
    )

    expect(screen.getByText(/Showing 81 to 95 of 95 results/)).toBeInTheDocument()
  })

  it('calls onPageChange when clicking page number', () => {
    const onPageChange = vi.fn()
    render(
      <Pagination page={1} pageSize={20} totalCount={100} onPageChange={onPageChange} />
    )

    fireEvent.click(screen.getByText('2'))
    expect(onPageChange).toHaveBeenCalledWith(2)
  })

  it('calls onPageChange when clicking Next', () => {
    const onPageChange = vi.fn()
    render(
      <Pagination page={1} pageSize={20} totalCount={100} onPageChange={onPageChange} />
    )

    fireEvent.click(screen.getByText('Next'))
    expect(onPageChange).toHaveBeenCalledWith(2)
  })

  it('calls onPageChange when clicking Previous', () => {
    const onPageChange = vi.fn()
    render(
      <Pagination page={2} pageSize={20} totalCount={100} onPageChange={onPageChange} />
    )

    fireEvent.click(screen.getByText('Previous'))
    expect(onPageChange).toHaveBeenCalledWith(1)
  })

  it('disables Previous on first page', () => {
    render(
      <Pagination page={1} pageSize={20} totalCount={100} onPageChange={() => {}} />
    )

    expect(screen.getByText('Previous')).toBeDisabled()
  })

  it('disables Next on last page', () => {
    render(
      <Pagination page={5} pageSize={20} totalCount={100} onPageChange={() => {}} />
    )

    expect(screen.getByText('Next')).toBeDisabled()
  })

  it('highlights current page', () => {
    render(
      <Pagination page={2} pageSize={20} totalCount={100} onPageChange={() => {}} />
    )

    const page2Button = screen.getByText('2')
    expect(page2Button).toHaveClass('bg-blue-500')
  })
})
