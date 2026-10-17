import { describe, it, expect } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import type { ReactNode } from 'react'
import { FiltersProvider, useFilters } from './useFilters'

// APM-5: build type and context filters.
describe('useFilters build type and context', () => {
  it('defaults to release builds and no context filters', () => {
    const { result } = renderHook(() => useFilters())
    expect(result.current.filters.buildType).toBe('release')
    expect(result.current.queryParams.build_type).toBe('release')
    expect(result.current.queryParams).not.toHaveProperty('ctx')
  })

  it('setBuildType updates queryParams', () => {
    const { result } = renderHook(() => useFilters())
    act(() => result.current.setBuildType('all'))
    expect(result.current.queryParams.build_type).toBe('all')
  })

  it('context filters become key:value params, without duplicates', () => {
    const { result } = renderHook(() => useFilters())
    act(() => {
      result.current.addContextFilter({ key: 'levelId', value: '3' })
      result.current.addContextFilter({ key: 'levelId', value: '4' })
      result.current.addContextFilter({ key: 'roomId', value: '' })
      result.current.addContextFilter({ key: 'levelId', value: '3' })
    })
    expect(result.current.queryParams.ctx).toEqual(['levelId:3', 'levelId:4', 'roomId:'])

    act(() => result.current.removeContextFilter(1))
    expect(result.current.queryParams.ctx).toEqual(['levelId:3', 'roomId:'])

    act(() => result.current.clearContextFilters())
    expect(result.current.queryParams).not.toHaveProperty('ctx')
  })

  it('FiltersProvider shares one state between components', () => {
    const wrapper = ({ children }: { children: ReactNode }) => <FiltersProvider>{children}</FiltersProvider>
    const { result } = renderHook(() => ({ a: useFilters(), b: useFilters() }), { wrapper })
    act(() => {
      result.current.a.setBuildType('dev')
      result.current.a.addContextFilter({ key: 'stageType', value: 'boss' })
    })
    expect(result.current.b.filters.buildType).toBe('dev')
    expect(result.current.b.queryParams.ctx).toEqual(['stageType:boss'])
  })
})
