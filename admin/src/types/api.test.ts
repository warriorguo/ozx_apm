import { describe, it, expect } from 'vitest'
import type {
  DashboardSummary,
  TimeSeriesResponse,
  DistributionResponse,
  CrashGroup,
  CrashListResponse,
  CrashDetail,
  ExceptionGroup,
  ExceptionListResponse,
} from './api'

describe('API Types', () => {
  it('DashboardSummary type is correctly structured', () => {
    const summary: DashboardSummary = {
      time_range: { start: '2024-01-01', end: '2024-01-02' },
      total_sessions: 1000,
      total_events: 50000,
      crash_count: 10,
      crash_rate: 0.01,
      exception_count: 100,
      jank_count: 50,
      avg_fps: 58.5,
      avg_startup_ms: 2500,
      top_versions: [],
      top_platforms: [],
    }

    expect(summary.total_sessions).toBe(1000)
    expect(summary.crash_rate).toBe(0.01)
  })

  it('TimeSeriesResponse type is correctly structured', () => {
    const response: TimeSeriesResponse = {
      metric: 'fps',
      data: [
        { timestamp: '2024-01-01T00:00:00Z', value: 60 },
        { timestamp: '2024-01-01T01:00:00Z', value: 59 },
      ],
    }

    expect(response.metric).toBe('fps')
    expect(response.data).toHaveLength(2)
  })

  it('DistributionResponse type is correctly structured', () => {
    const response: DistributionResponse = {
      metric: 'frame_time',
      buckets: [
        { bucket: '0-10', count: 100, pct: 10 },
        { bucket: '10-20', count: 500, pct: 50 },
      ],
      p50: 15,
      p90: 25,
      p95: 28,
      p99: 32,
    }

    expect(response.metric).toBe('frame_time')
    expect(response.p50).toBe(15)
  })

  it('CrashGroup type is correctly structured', () => {
    const crash: CrashGroup = {
      fingerprint: 'crash-123',
      crash_type: 'SIGSEGV',
      sample_message: 'Segmentation fault',
      count: 100,
      session_count: 50,
      first_seen: '2024-01-01T00:00:00Z',
      last_seen: '2024-01-15T00:00:00Z',
      affected_versions: ['1.0.0', '1.1.0'],
      top_devices: ['Pixel 6', 'Galaxy S21'],
    }

    expect(crash.fingerprint).toBe('crash-123')
    expect(crash.affected_versions).toHaveLength(2)
  })

  it('CrashListResponse type is correctly structured', () => {
    const response: CrashListResponse = {
      crashes: [],
      total_count: 100,
      page: 1,
      page_size: 20,
    }

    expect(response.total_count).toBe(100)
    expect(response.page).toBe(1)
  })

  it('CrashDetail type is correctly structured', () => {
    const detail: CrashDetail = {
      fingerprint: 'crash-123',
      crash_type: 'SIGSEGV',
      stack: 'at NativeMethod()',
      count: 100,
      session_count: 50,
      first_seen: '2024-01-01T00:00:00Z',
      last_seen: '2024-01-15T00:00:00Z',
      occurrences: [],
      version_distribution: [],
      device_distribution: [],
      os_distribution: [],
    }

    expect(detail.fingerprint).toBe('crash-123')
    expect(detail.stack).toContain('NativeMethod')
  })

  it('ExceptionGroup type is correctly structured', () => {
    const exception: ExceptionGroup = {
      fingerprint: 'exc-123',
      message: 'NullReferenceException',
      count: 500,
      session_count: 200,
      first_seen: '2024-01-01T00:00:00Z',
      last_seen: '2024-01-15T00:00:00Z',
    }

    expect(exception.fingerprint).toBe('exc-123')
    expect(exception.count).toBe(500)
  })

  it('ExceptionListResponse type is correctly structured', () => {
    const response: ExceptionListResponse = {
      exceptions: [],
      total_count: 500,
      page: 1,
      page_size: 20,
    }

    expect(response.total_count).toBe(500)
  })
})
