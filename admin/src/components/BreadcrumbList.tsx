import { parseBreadcrumb, kindStyles } from '../utils/timeline'

interface BreadcrumbListProps {
  lines: string[]
  // Shown after the last line: the occurrence the breadcrumbs lead to.
  endLabel?: string
}

// The breadcrumbs an exception carries (APM-4/APM-6) as a time line, oldest
// first: custom events, scene loads, the recent Warning/Error lines and
// earlier exceptions, each marked by kind. Lines are game text: rendered as
// text only.
export function BreadcrumbList({ lines, endLabel }: BreadcrumbListProps) {
  if (lines.length === 0) {
    return <p className="text-xs text-gray-400">No breadcrumbs (sent with the first occurrence of a window only; older SDKs send none).</p>
  }
  return (
    <ol className="border-l-2 border-gray-200 ml-1 space-y-1" aria-label="Breadcrumbs">
      {lines.map((line, i) => {
        const b = parseBreadcrumb(line)
        const style = kindStyles[b.kind]
        return (
          <li key={i} className="relative pl-3 text-xs" data-kind={b.kind}>
            <span className="absolute -left-[5px] top-1.5 w-2 h-2 rounded-full bg-gray-300" />
            <span className="font-mono text-gray-400 mr-2">{b.clock || '--:--:--.---'}</span>
            <span className={`inline-block px-1.5 rounded mr-2 ${style.className}`}>{style.label}</span>
            <span className="font-mono text-gray-800 break-all">{b.text}</span>
            {b.repeats > 0 && (
              <span className="ml-2 text-gray-500" title={`Logged ${b.repeats} times, the last at ${b.until} UTC`}>
                ×{b.repeats} until {b.until}
              </span>
            )}
          </li>
        )
      })}
      {endLabel && (
        <li className="relative pl-3 text-xs font-medium text-red-700">
          <span className="absolute -left-[5px] top-1.5 w-2 h-2 rounded-full bg-red-500" />
          {endLabel}
        </li>
      )}
    </ol>
  )
}
