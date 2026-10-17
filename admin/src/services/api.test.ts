import { describe, it, expect, vi, beforeEach } from 'vitest'

// Use vi.hoisted to ensure mockGet is available when vi.mock runs
const { mockGet } = vi.hoisted(() => {
  return { mockGet: vi.fn() }
})

vi.mock('axios', () => {
  return {
    default: {
      create: () => ({
        get: mockGet,
      }),
    },
  }
})

// Import after mocking
import {
  getSummary,
  getTimeSeries,
  getDistribution,
  getAppVersions,
  getScenes,
  getCrashes,
  getCrashDetail,
  getExceptions,
  getContextKeys,
  getContextValues,
  getContextDistribution,
  getExceptionDetail,
  getExceptionOccurrences,
  getExceptionDistribution,
  getSessionTimeline,
  getDeviceSessions,
} from './api'

describe('API Service', () => {
  beforeEach(() => {
    mockGet.mockReset()
  })

  describe('getSummary', () => {
    it('calls API with correct params', async () => {
      const mockData = {
        total_sessions: 1000,
        crash_rate: 0.01,
      }
      mockGet.mockResolvedValue({ data: mockData })

      const params = { start_time: '2024-01-01', end_time: '2024-01-02' }
      const result = await getSummary(params)

      expect(mockGet).toHaveBeenCalledWith('/summary', { params })
      // Missing arrays are normalized to [].
      expect(result).toEqual({ ...mockData, top_versions: [], top_platforms: [] })
    })
  })

  describe('getTimeSeries', () => {
    it('calls API with metric and params', async () => {
      const mockData = { metric: 'fps', data: [] }
      mockGet.mockResolvedValue({ data: mockData })

      const params = { start_time: '2024-01-01', end_time: '2024-01-02' }
      const result = await getTimeSeries('fps', params)

      expect(mockGet).toHaveBeenCalledWith('/timeseries', {
        params: { metric: 'fps', ...params },
      })
      expect(result).toEqual(mockData)
    })
  })

  describe('getDistribution', () => {
    it('calls API with metric and params', async () => {
      const mockData = { metric: 'fps', buckets: [], p50: 0, p90: 0, p95: 0, p99: 0 }
      mockGet.mockResolvedValue({ data: mockData })

      const params = { start_time: '2024-01-01', end_time: '2024-01-02', scene: 'GamePlay' }
      const result = await getDistribution('fps', params)

      expect(mockGet).toHaveBeenCalledWith('/distribution', {
        params: { metric: 'fps', ...params },
      })
      expect(result).toEqual(mockData)
    })
  })

  describe('getAppVersions', () => {
    it('calls API and extracts versions array', async () => {
      const mockData = { versions: ['1.0.0', '1.1.0', '1.2.0'] }
      mockGet.mockResolvedValue({ data: mockData })

      const result = await getAppVersions()

      expect(mockGet).toHaveBeenCalledWith('/versions')
      expect(result).toEqual(['1.0.0', '1.1.0', '1.2.0'])
    })
  })

  describe('getScenes', () => {
    it('calls API with optional appVersion', async () => {
      const mockData = { scenes: ['MainMenu', 'GamePlay'] }
      mockGet.mockResolvedValue({ data: mockData })

      const result = await getScenes('1.0.0')

      expect(mockGet).toHaveBeenCalledWith('/scenes', {
        params: { app_version: '1.0.0' },
      })
      expect(result).toEqual(['MainMenu', 'GamePlay'])
    })

    it('calls API without appVersion', async () => {
      const mockData = { scenes: ['MainMenu'] }
      mockGet.mockResolvedValue({ data: mockData })

      const result = await getScenes()

      expect(mockGet).toHaveBeenCalledWith('/scenes', {
        params: { app_version: undefined },
      })
      expect(result).toEqual(['MainMenu'])
    })
  })

  describe('getCrashes', () => {
    it('calls API with pagination params', async () => {
      const mockData = { crashes: [], total_count: 100, page: 1, page_size: 20 }
      mockGet.mockResolvedValue({ data: mockData })

      const params = { start_time: '2024-01-01', page: 1, page_size: 20 }
      const result = await getCrashes(params)

      expect(mockGet).toHaveBeenCalledWith('/crashes', { params })
      expect(result).toEqual(mockData)
    })
  })

  describe('getCrashDetail', () => {
    it('calls API with fingerprint', async () => {
      const mockData = { fingerprint: 'crash-123', crash_type: 'SIGSEGV' }
      mockGet.mockResolvedValue({ data: mockData })

      const params = { start_time: '2024-01-01', end_time: '2024-01-02' }
      const result = await getCrashDetail('crash-123', params)

      expect(mockGet).toHaveBeenCalledWith('/crashes/detail', {
        params: { fingerprint: 'crash-123', ...params },
      })
      // Missing arrays are normalized to [].
      expect(result).toEqual({
        ...mockData,
        occurrences: [],
        version_distribution: [],
        device_distribution: [],
        os_distribution: [],
      })
    })

    it('normalizes occurrences without context (servers before APM-5)', async () => {
      mockGet.mockResolvedValue({
        data: {
          fingerprint: 'c',
          occurrences: [{ timestamp: 't', breadcrumbs: null }],
        },
      })
      const result = await getCrashDetail('c', {})
      expect(result.occurrences[0].breadcrumbs).toEqual([])
      expect(result.occurrences[0].context).toEqual({})
    })
  })

  describe('getExceptions', () => {
    it('calls API with pagination params', async () => {
      const mockData = { exceptions: [], total_count: 500, page: 1, page_size: 20 }
      mockGet.mockResolvedValue({ data: mockData })

      const params = { start_time: '2024-01-01', page: 2, page_size: 50 }
      const result = await getExceptions(params)

      expect(mockGet).toHaveBeenCalledWith('/exceptions', { params })
      expect(result).toEqual(mockData)
    })
  })

  describe('context APIs (APM-5)', () => {
    it('getContextKeys passes the filter and normalizes null', async () => {
      mockGet.mockResolvedValue({ data: { keys: null } })
      const params = { build_type: 'all', ctx: ['levelId:3', 'roomId:r1'] }
      const result = await getContextKeys(params)
      expect(mockGet).toHaveBeenCalledWith('/context/keys', { params })
      expect(result).toEqual({ keys: [] })
    })

    it('getContextValues sends the key', async () => {
      mockGet.mockResolvedValue({ data: { key: 'levelId', values: [{ value: '1', count: 2 }] } })
      const result = await getContextValues('levelId', { build_type: 'release' })
      expect(mockGet).toHaveBeenCalledWith('/context/values', {
        params: { key: 'levelId', build_type: 'release' },
      })
      expect(result.values).toEqual([{ value: '1', count: 2 }])
    })

    it('getContextDistribution survives an empty body', async () => {
      mockGet.mockResolvedValue({ data: null })
      const result = await getContextDistribution('levelId', 'fps', {})
      expect(mockGet).toHaveBeenCalledWith('/context/distribution', {
        params: { key: 'levelId', metric: 'fps' },
      })
      expect(result).toEqual({ key: 'levelId', metric: 'fps', unit: '', total: 0, truncated: false, rows: [] })
    })
  })

  describe('exception detail APIs (APM-11)', () => {
    it('getExceptionDetail fills what an old server or an old row lacks', async () => {
      mockGet.mockResolvedValue({ data: { fingerprint: 'f', count: 3, occurrences: [{ timestamp: 't' }], sample: null } })
      const d = await getExceptionDetail('f', { build_type: 'all' })
      expect(mockGet).toHaveBeenCalledWith('/exceptions/detail', { params: { fingerprint: 'f', build_type: 'all' } })
      expect(d.sample.parsed.chain).toEqual([])
      expect(d.sample.parsed.log_frames).toEqual([])
      expect(d.sample.inner_exceptions).toEqual([])
      expect(d.trend).toEqual({ interval_seconds: 0, points: [] })
      expect(d.occurrences[0]).toMatchObject({ context: {}, breadcrumbs: [], error_seq: 0, ctx_merged: false, device_id: '' })
      expect(d.version_distribution).toEqual([])
    })

    it('getExceptionDetail rejects an empty body', async () => {
      mockGet.mockResolvedValue({ data: null })
      await expect(getExceptionDetail('f', {})).rejects.toThrow()
    })

    it('getExceptionOccurrences pages and normalizes', async () => {
      mockGet.mockResolvedValue({ data: { occurrences: [{ count: 2, context: null }], total_count: 7, page: 2, page_size: 5 } })
      const r = await getExceptionOccurrences('f', { page: 2, page_size: 5 })
      expect(mockGet).toHaveBeenCalledWith('/exceptions/occurrences', { params: { fingerprint: 'f', page: 2, page_size: 5 } })
      expect(r.total_count).toBe(7)
      expect(r.occurrences[0].context).toEqual({})
    })

    it('getExceptionDistribution sends by or key', async () => {
      mockGet.mockResolvedValue({ data: null })
      const r = await getExceptionDistribution('f', { kind: 'key', key: 'roomId' }, {})
      expect(mockGet).toHaveBeenCalledWith('/exceptions/distribution', { params: { fingerprint: 'f', key: 'roomId' } })
      expect(r).toEqual({ fingerprint: 'f', group_by: '', total: 0, merged: 0, truncated: false, rows: [] })
      await getExceptionDistribution('f', { kind: 'by', key: 'build_type' }, { build_type: 'all' })
      expect(mockGet).toHaveBeenLastCalledWith('/exceptions/distribution', { params: { fingerprint: 'f', by: 'build_type', build_type: 'all' } })
    })

    it('getExceptions passes the sort and first_error', async () => {
      mockGet.mockResolvedValue({ data: { exceptions: null, total_count: 0, page: 1, page_size: 20 } })
      const r = await getExceptions({ sort: 'first_seen', first_error: '1' })
      expect(mockGet).toHaveBeenCalledWith('/exceptions', { params: { sort: 'first_seen', first_error: '1' } })
      expect(r.exceptions).toEqual([])
    })
  })

  describe('session APIs (APM-15)', () => {
    it('getSessionTimeline survives an empty body', async () => {
      mockGet.mockResolvedValue({ data: null })
      const t = await getSessionTimeline({ session_id: 's', at: '2026-10-18T10:00:00Z' })
      expect(mockGet).toHaveBeenCalledWith('/sessions/timeline', { params: { session_id: 's', at: '2026-10-18T10:00:00Z' } })
      expect(t).toMatchObject({ session_id: 's', found: false, items: [], truncated: [], perf: [], counts: {} })
      expect(t.session.session_id).toBe('s')
    })

    it('getDeviceSessions sends the device and the range', async () => {
      mockGet.mockResolvedValue({ data: { sessions: null } })
      const r = await getDeviceSessions('d', { start_time: 'a' })
      expect(mockGet).toHaveBeenCalledWith('/sessions', { params: { device_id: 'd', start_time: 'a' } })
      expect(r.sessions).toEqual([])
    })
  })
})
