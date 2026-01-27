import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
} from 'recharts'
import type { DistributionResponse } from '../types/api'

interface DistributionChartProps {
  data: DistributionResponse | null | undefined
  title: string
  color?: string
}

export function DistributionChart({
  data,
  title,
  color = '#3b82f6',
}: DistributionChartProps) {
  if (!data || !data.buckets || data.buckets.length === 0) {
    return (
      <div className="bg-white rounded-lg shadow p-6">
        <h3 className="text-sm font-medium text-gray-700 mb-4">{title}</h3>
        <div className="h-64 flex items-center justify-center text-gray-400 text-sm">
          No data available
        </div>
      </div>
    )
  }

  return (
    <div className="bg-white rounded-lg shadow p-6">
      <div className="flex items-start justify-between mb-4">
        <h3 className="text-sm font-medium text-gray-700">{title}</h3>
        <div className="flex gap-4 text-xs text-gray-500">
          <span>P50: {data.p50?.toFixed(1) ?? '-'}</span>
          <span>P90: {data.p90?.toFixed(1) ?? '-'}</span>
          <span>P95: {data.p95?.toFixed(1) ?? '-'}</span>
          <span>P99: {data.p99?.toFixed(1) ?? '-'}</span>
        </div>
      </div>
      <div className="h-64">
        <ResponsiveContainer width="100%" height="100%">
          <BarChart data={data.buckets} margin={{ top: 5, right: 20, left: 10, bottom: 5 }}>
            <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" />
            <XAxis
              dataKey="bucket"
              tick={{ fontSize: 10 }}
              tickLine={false}
              axisLine={{ stroke: '#e5e7eb' }}
              angle={-45}
              textAnchor="end"
              height={60}
            />
            <YAxis
              tick={{ fontSize: 11 }}
              tickLine={false}
              axisLine={{ stroke: '#e5e7eb' }}
            />
            <Tooltip
              formatter={(value: number) => [value.toLocaleString(), 'Count']}
              labelFormatter={(label) => `Range: ${label}`}
              contentStyle={{ fontSize: 12 }}
            />
            <Bar dataKey="count" fill={color} radius={[4, 4, 0, 0]} />
          </BarChart>
        </ResponsiveContainer>
      </div>
    </div>
  )
}
