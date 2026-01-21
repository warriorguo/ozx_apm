import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
} from 'recharts'
import { format, parseISO } from 'date-fns'
import type { TimeSeriesPoint } from '../types/api'

interface TimeSeriesChartProps {
  data: TimeSeriesPoint[]
  title: string
  color?: string
  yAxisLabel?: string
  formatValue?: (value: number) => string
}

export function TimeSeriesChart({
  data,
  title,
  color = '#3b82f6',
  yAxisLabel,
  formatValue = (v) => v.toFixed(1),
}: TimeSeriesChartProps) {
  const chartData = data.map((point) => ({
    timestamp: point.timestamp,
    value: point.value,
    time: format(parseISO(point.timestamp), 'HH:mm'),
  }))

  return (
    <div className="bg-white rounded-lg shadow p-6">
      <h3 className="text-sm font-medium text-gray-700 mb-4">{title}</h3>
      <div className="h-64">
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={chartData} margin={{ top: 5, right: 20, left: 10, bottom: 5 }}>
            <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" />
            <XAxis
              dataKey="time"
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
              labelFormatter={(label) => `Time: ${label}`}
              contentStyle={{ fontSize: 12 }}
            />
            <Line
              type="monotone"
              dataKey="value"
              stroke={color}
              strokeWidth={2}
              dot={false}
              activeDot={{ r: 4 }}
            />
          </LineChart>
        </ResponsiveContainer>
      </div>
    </div>
  )
}
