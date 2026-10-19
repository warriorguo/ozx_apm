import { describe, it, expect, vi, beforeEach } from 'vitest'

const { mockGet } = vi.hoisted(() => ({ mockGet: vi.fn() }))

vi.mock('axios', () => ({ default: { create: () => ({ get: mockGet }) } }))

import { getContextDistribution, getFunnel, getMemoryBreakdown, getRunPeaks, getSessionHealth } from './api'

const filter = { start_time: 's', end_time: 'e', build_type: 'release', ctx: ['roomId:3'] }

describe('APM-9 API calls', () => {
  beforeEach(() => mockGet.mockReset())

  it('sends the group, metric and common filter', async () => {
    mockGet.mockResolvedValue({ data: { group_by: 'ctx:stageType', rows: [{ value: '2.0', samples: 3, sessions: 1 }] } })
    const bd = await getMemoryBreakdown({ kind: 'key', key: 'stageType' }, filter)
    expect(mockGet).toHaveBeenCalledWith('/memory/breakdown', { params: { key: 'stageType', ...filter } })
    expect(bd.rows[0].figures).toEqual({}) // missing figures read as none
    expect(bd.truncated).toBe(false)

    mockGet.mockResolvedValue({ data: null })
    const peaks = await getRunPeaks('peak_alloc', { kind: 'by', key: 'app_version' }, filter)
    expect(mockGet).toHaveBeenLastCalledWith('/memory/runs', { params: { metric: 'peak_alloc', by: 'app_version', ...filter } })
    expect(peaks).toEqual({ metric: 'peak_alloc', group_by: '', rows: [], buckets: [], truncated: false })
  })

  it('sends crash_types only when chosen', async () => {
    mockGet.mockResolvedValue({ data: { rows: [{ value: '1', build_type: 'release', sessions: 1 }] } })
    const h = await getSessionHealth({ kind: 'by', key: 'app_version' }, ['native', 'oom'], filter)
    expect(mockGet).toHaveBeenCalledWith('/health/sessions', { params: { by: 'app_version', crash_types: 'native,oom', ...filter } })
    expect(h.rows[0].crashes_by_type).toEqual({})
    await getSessionHealth({ kind: 'key', key: 'dataVersion' }, [], filter)
    expect(mockGet).toHaveBeenLastCalledWith('/health/sessions', { params: { key: 'dataVersion', ...filter } })
  })

  it('sends the funnel steps as one JSON parameter', async () => {
    mockGet.mockResolvedValue({ data: { groups: [{ value: '', steps: null }] } })
    const steps = [{ name: 'run_start' }, { name: 'room_enter', props: { roomType: 'boss' } }]
    const f = await getFunnel(steps, 3600, 'app_version', filter)
    expect(mockGet).toHaveBeenCalledWith('/funnel', {
      params: { steps: '[{"name":"run_start"},{"name":"room_enter","props":{"roomType":"boss"}}]', window: 3600, by: 'app_version', ...filter },
    })
    expect(f.groups[0].steps).toEqual([])
    expect(f.window_seconds).toBe(3600)
    await getFunnel(steps, 60, '', filter)
    expect(mockGet.mock.calls[1][1].params.by).toBeUndefined()
  })

  it('passes bucket=count to the context distribution', async () => {
    mockGet.mockResolvedValue({ data: { rows: [], bucket: 'count' } })
    const d = await getContextDistribution('aliveEnemies', 'jank_rate', { ...filter, bucket: 'count' })
    expect(mockGet).toHaveBeenCalledWith('/context/distribution', { params: { key: 'aliveEnemies', metric: 'jank_rate', ...filter, bucket: 'count' } })
    expect(d.bucket).toBe('count')
  })
})
