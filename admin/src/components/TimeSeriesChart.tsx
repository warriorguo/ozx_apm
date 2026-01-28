import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
} from 'recharts'
import { format, parseISO, differenceInDays, differenceInHours } from 'date-fns'
import type { TimeSeriesPoint } from '../types/api'

interface TimeSeriesChartProps {
  data: TimeSeriesPoint[] | null | undefined
  title: string
  startTime?: string
  endTime?: string
  color?: string
  yAxisLabel?: string
  formatValue?: (value: number) => string
}

interface ChartDataPoint {
  timestamp: number
  value: number | null
}

export function TimeSeriesChart({
  data,
  title,
  startTime,
  endTime,
  color = '#3b82f6',
  yAxisLabel,
  formatValue = (v) => v.toFixed(1),
}: TimeSeriesChartProps) {
  if (!data || data.length === 0) {
    return (
      <div className="bg-white rounded-lg shadow p-6">
        <h3 className="text-sm font-medium text-gray-700 mb-4">{title}</h3>
        <div className="h-64 flex items-center justify-center text-gray-400 text-sm">
          No data available
        </div>
      </div>
    )
  }

  // Determine time format and interval based on range
  const getTimeFormatAndInterval = () => {
    if (!startTime || !endTime) return { format: 'HH:mm', intervalMs: 60 * 60 * 1000 }
    const start = parseISO(startTime)
    const end = parseISO(endTime)
    const days = differenceInDays(end, start)
    const hours = differenceInHours(end, start)

    if (days > 7) return { format: 'MM/dd', intervalMs: 24 * 60 * 60 * 1000 }
    if (hours < 6) return { format: 'HH:mm', intervalMs: 5 * 60 * 1000 }
    if (days > 1) return { format: 'MM/dd HH:mm', intervalMs: 60 * 60 * 1000 }
    return { format: 'HH:mm', intervalMs: 60 * 60 * 1000 }
  }

  const { format: timeFormat, intervalMs } = getTimeFormatAndInterval()

  // Build chart data with null gaps for discontinuous data
  const chartData: ChartDataPoint[] = []
  const sortedData = [...data].sort(
    (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
  )

  for (let i = 0; i < sortedData.length; i++) {
    const point = sortedData[i]
    const ts = new Date(point.timestamp).getTime()

    // Insert null point if gap is too large (more than 1.5x interval)
    if (i > 0) {
      const prevTs = new Date(sortedData[i - 1].timestamp).getTime()
      const gap = ts - prevTs
      if (gap > intervalMs * 1.5) {
        // Insert a null point to break the line
        chartData.push({ timestamp: prevTs + intervalMs, value: null })
      }
    }

    chartData.push({ timestamp: ts, value: point.value })
  }

  // Calculate domain for x-axis based on startTime/endTime
  const domain: [number, number] | undefined =
    startTime && endTime
      ? [new Date(startTime).getTime(), new Date(endTime).getTime()]
      : undefined

  const formatXAxis = (timestamp: number) => {
    return format(new Date(timestamp), timeFormat)
  }

  return (
    <div className="bg-white rounded-lg shadow p-6">
      <h3 className="text-sm font-medium text-gray-700 mb-4">{title}</h3>
      <div className="h-64">
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={chartData} margin={{ top: 5, right: 20, left: 10, bottom: 5 }}>
            <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" />
            <XAxis
              dataKey="timestamp"
              type="number"
              scale="time"
              domain={domain}
              tickFormatter={formatXAxis}
              tick={{ fontSize: 11 }}
              tickLine={false}
              axisLine={{ stroke: '#e5e7eb' }}
            />
            <YAxis
              tick={{ fontSize: 11 }}
              tickLine={false}
              axisLine={{ stroke: '#e5e7eb' }}
              label={
                yAxisLabel
                  ? { value: yAxisLabel, angle: -90, position: 'insideLeft', fontSize: 11 }
                  : undefined
              }
            />
            <Tooltip
              formatter={(value: number) => [formatValue(value), title]}
              labelFormatter={(ts: number) =>
                `Time: ${format(new Date(ts), 'yyyy-MM-dd HH:mm')}`
              }
              contentStyle={{ fontSize: 12 }}
            />
            <Line
              type="monotone"
              dataKey="value"
              stroke={color}
              strokeWidth={2}
              dot={{ r: 3, fill: color }}
              activeDot={{ r: 5 }}
              connectNulls={false}
            />
          </LineChart>
        </ResponsiveContainer>
      </div>
    </div>
  )
}
