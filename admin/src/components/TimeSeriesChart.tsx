import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
} from 'recharts'
import { format, parseISO, differenceInDays } from 'date-fns'
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

  // Determine time format based on range
  const getTimeFormat = () => {
    if (!startTime || !endTime) return 'HH:mm'
    const start = parseISO(startTime)
    const end = parseISO(endTime)
    const days = differenceInDays(end, start)

    if (days > 7) return 'MM/dd'
    if (days > 1) return 'MM/dd HH:mm'
    return 'HH:mm'
  }

  const timeFormat = getTimeFormat()

  const chartData = data.map((point) => ({
    timestamp: new Date(point.timestamp).getTime(),
    value: point.value,
    time: format(parseISO(point.timestamp), timeFormat),
  }))

  // Calculate domain for x-axis based on startTime/endTime
  const domain: [number, number] | undefined = startTime && endTime
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
              labelFormatter={(ts: number) => `Time: ${format(new Date(ts), 'yyyy-MM-dd HH:mm')}`}
              contentStyle={{ fontSize: 12 }}
            />
            <Line
              type="monotone"
              dataKey="value"
              stroke={color}
              strokeWidth={2}
              dot={false}
              activeDot={{ r: 4 }}
              connectNulls={false}
            />
          </LineChart>
        </ResponsiveContainer>
      </div>
    </div>
  )
}
