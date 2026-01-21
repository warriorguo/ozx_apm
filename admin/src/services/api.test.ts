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
      expect(result).toEqual(mockData)
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
      expect(result).toEqual(mockData)
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
})
