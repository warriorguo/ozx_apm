interface StatCardProps {
  title: string
  value: string | number
  subtitle?: string
  trend?: 'up' | 'down' | 'neutral'
  trendValue?: string
  color?: 'default' | 'success' | 'warning' | 'danger'
}

const colorClasses = {
  default: 'bg-blue-500',
  success: 'bg-green-500',
  warning: 'bg-yellow-500',
  danger: 'bg-red-500',
}

export function StatCard({
  title,
  value,
  subtitle,
  trend,
  trendValue,
  color = 'default',
}: StatCardProps) {
  return (
    <div className="bg-white rounded-lg shadow p-6">
      <div className="flex items-start justify-between">
        <div>
          <p className="text-sm text-gray-500 mb-1">{title}</p>
          <p className="text-2xl font-bold text-gray-900">{value}</p>
          {subtitle && <p className="text-xs text-gray-400 mt-1">{subtitle}</p>}
        </div>
        <div className={`w-3 h-3 rounded-full ${colorClasses[color]}`} />
      </div>
      {trend && trendValue && (
        <div className="mt-2 flex items-center gap-1">
          <span
            className={`text-xs ${
              trend === 'up' ? 'text-red-500' : trend === 'down' ? 'text-green-500' : 'text-gray-500'
            }`}
          >
            {trend === 'up' ? '+' : trend === 'down' ? '-' : ''}
            {trendValue}
          </span>
          <span className="text-xs text-gray-400">vs previous</span>
        </div>
      )}
    </div>
  )
}
