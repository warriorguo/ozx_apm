import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { DataTable } from './DataTable'

interface TestItem {
  id: number
  name: string
  value: number
}

describe('DataTable', () => {
  const columns = [
    { key: 'id', header: 'ID' },
    { key: 'name', header: 'Name' },
    { key: 'value', header: 'Value' },
  ]

  const data: TestItem[] = [
    { id: 1, name: 'Item 1', value: 100 },
    { id: 2, name: 'Item 2', value: 200 },
  ]

  it('renders column headers', () => {
    render(<DataTable data={data} columns={columns} />)

    expect(screen.getByText('ID')).toBeInTheDocument()
    expect(screen.getByText('Name')).toBeInTheDocument()
    expect(screen.getByText('Value')).toBeInTheDocument()
  })

  it('renders data rows', () => {
    render(<DataTable data={data} columns={columns} />)

    expect(screen.getByText('Item 1')).toBeInTheDocument()
    expect(screen.getByText('Item 2')).toBeInTheDocument()
    expect(screen.getByText('100')).toBeInTheDocument()
    expect(screen.getByText('200')).toBeInTheDocument()
  })

  it('shows empty message when no data', () => {
    render(<DataTable data={[]} columns={columns} emptyMessage="No items found" />)

    expect(screen.getByText('No items found')).toBeInTheDocument()
  })

  it('shows default empty message', () => {
    render(<DataTable data={[]} columns={columns} />)

    expect(screen.getByText('No data available')).toBeInTheDocument()
  })

  it('shows loading state', () => {
    render(<DataTable data={[]} columns={columns} isLoading={true} />)

    expect(screen.getByText('Loading...')).toBeInTheDocument()
  })

  it('calls onRowClick when row is clicked', () => {
    const onRowClick = vi.fn()
    render(<DataTable data={data} columns={columns} onRowClick={onRowClick} />)

    fireEvent.click(screen.getByText('Item 1'))
    expect(onRowClick).toHaveBeenCalledWith(data[0])
  })

  it('uses custom render function when provided', () => {
    const columnsWithRender = [
      {
        key: 'value',
        header: 'Value',
        render: (item: TestItem) => <span data-testid="custom">${item.value}</span>,
      },
    ]

    render(<DataTable data={data} columns={columnsWithRender} />)

    expect(screen.getByText('$100')).toBeInTheDocument()
    expect(screen.getAllByTestId('custom')).toHaveLength(2)
  })

  it('applies className to columns', () => {
    const columnsWithClass = [
      { key: 'name', header: 'Name', className: 'custom-class' },
    ]

    const { container } = render(<DataTable data={data} columns={columnsWithClass} />)

    const cells = container.querySelectorAll('.custom-class')
    expect(cells.length).toBeGreaterThan(0)
  })
})
