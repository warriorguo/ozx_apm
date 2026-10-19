import { useState } from 'react'
import type { FormEvent } from 'react'
import { GlobalFilterBar } from '../components/FilterBar'
import { Clipped } from '../components/GroupPicker'
import { useFilters } from '../hooks/useFilters'
import { isValidContextKey, useCustomEvents, useFunnel } from '../hooks/useApi'
import { formatNumber, formatPercent, requestError } from '../utils/format'
import type { FunnelSplit, FunnelStep } from '../types/api'

export const maxSteps = 8
export const maxProps = 4
const maxWindowSeconds = 24 * 3600

interface DraftProp {
  key: string
  value: string
}

interface DraftStep {
  name: string
  props: DraftProp[]
}

// The steps to send, or the first problem found (for the form).
export function buildSteps(draft: DraftStep[]): { steps: FunnelStep[]; error: string } {
  if (draft.length === 0) return { steps: [], error: 'Add at least one step.' }
  if (draft.length > maxSteps) return { steps: [], error: `At most ${maxSteps} steps.` }
  const steps: FunnelStep[] = []
  for (let i = 0; i < draft.length; i++) {
    const d = draft[i]
    const name = d.name.trim()
    if (!isValidContextKey(name)) {
      return { steps: [], error: `Step ${i + 1}: the event name must be 1-64 characters from A-Z a-z 0-9 _ . -` }
    }
    const props: Record<string, string> = {}
    for (const p of d.props) {
      const key = p.key.trim()
      if (key === '' && p.value === '') continue
      if (!isValidContextKey(key)) {
        return { steps: [], error: `Step ${i + 1}: property keys must be 1-64 characters from A-Z a-z 0-9 _ . -` }
      }
      if ([...p.value].length > 256) return { steps: [], error: `Step ${i + 1}: property values up to 256 characters.` }
      props[key] = p.value
    }
    if (Object.keys(props).length > maxProps) return { steps: [], error: `Step ${i + 1}: at most ${maxProps} properties.` }
    steps.push(Object.keys(props).length > 0 ? { name, props } : { name })
  }
  return { steps, error: '' }
}

const emptyStep = (): DraftStep => ({ name: '', props: [] })

function propsText(props?: Record<string, string>): string {
  if (!props) return ''
  return Object.entries(props)
    .map(([k, v]) => `${k}=${v}`)
    .join(', ')
}

// Funnel (APM-9): sessions that went through custom events (ApmClient.TrackEvent)
// in order, within a window, overall or per version / build type / platform.
export function Funnel() {
  const { queryParams } = useFilters()
  const [draft, setDraft] = useState<DraftStep[]>([emptyStep(), emptyStep()])
  const [windowValue, setWindowValue] = useState(2)
  const [windowUnit, setWindowUnit] = useState<'m' | 'h'>('h')
  const [by, setBy] = useState<FunnelSplit>('')
  const [error, setError] = useState('')
  const [request, setRequest] = useState<{ steps: FunnelStep[]; windowSeconds: number; by: FunnelSplit } | null>(null)
  const events = useCustomEvents(queryParams)
  const { data, isLoading, isError, isFetching, error: queryError } = useFunnel(request, queryParams)
  const eventNames = (events.data?.events ?? []).map((e) => e.name)

  const update = (i: number, f: (s: DraftStep) => DraftStep) => setDraft((prev) => prev.map((s, j) => (j === i ? f(s) : s)))
  const move = (i: number, d: number) =>
    setDraft((prev) => {
      const j = i + d
      if (j < 0 || j >= prev.length) return prev
      const next = [...prev]
      ;[next[i], next[j]] = [next[j], next[i]]
      return next
    })

  const run = (e: FormEvent) => {
    e.preventDefault()
    const { steps, error: err } = buildSteps(draft)
    const seconds = Math.round(windowValue * (windowUnit === 'h' ? 3600 : 60))
    if (err) {
      setError(err)
      return
    }
    if (!(seconds >= 1 && seconds <= maxWindowSeconds)) {
      setError('The window must be between 1 minute and 24 hours.')
      return
    }
    setError('')
    setRequest({ steps, windowSeconds: seconds, by })
  }

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Funnel</h1>
      <GlobalFilterBar />
      <form onSubmit={run} className="bg-white rounded-lg shadow p-4 mb-6 space-y-3">
        <datalist id="funnel-event-names">
          {eventNames.map((n) => (
            <option key={n} value={n} />
          ))}
        </datalist>
        {draft.map((s, i) => (
          <div key={i} className="border border-gray-200 rounded p-3" data-testid="funnel-step">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-sm font-medium text-gray-700 w-14">Step {i + 1}</span>
              <input
                aria-label={`Step ${i + 1} event`}
                list="funnel-event-names"
                value={s.name}
                onChange={(e) => update(i, (x) => ({ ...x, name: e.target.value }))}
                placeholder="event name"
                maxLength={64}
                className="px-2 py-1 text-sm border border-gray-300 rounded font-mono"
              />
              <button type="button" onClick={() => move(i, -1)} disabled={i === 0} className="text-xs text-gray-600 disabled:opacity-30" aria-label={`Move step ${i + 1} up`}>
                ↑
              </button>
              <button type="button" onClick={() => move(i, 1)} disabled={i === draft.length - 1} className="text-xs text-gray-600 disabled:opacity-30" aria-label={`Move step ${i + 1} down`}>
                ↓
              </button>
              <button
                type="button"
                onClick={() => update(i, (x) => ({ ...x, props: [...x.props, { key: '', value: '' }] }))}
                disabled={s.props.length >= maxProps}
                className="text-xs text-blue-600 disabled:opacity-30"
              >
                + property
              </button>
              <button type="button" onClick={() => setDraft((prev) => prev.filter((_, j) => j !== i))} className="text-xs text-red-600" aria-label={`Remove step ${i + 1}`}>
                remove
              </button>
            </div>
            {s.props.map((p, k) => (
              <div key={k} className="flex items-center gap-2 mt-2 ml-16">
                <input
                  aria-label={`Step ${i + 1} property ${k + 1} key`}
                  value={p.key}
                  onChange={(e) => update(i, (x) => ({ ...x, props: x.props.map((q, m) => (m === k ? { ...q, key: e.target.value } : q)) }))}
                  placeholder="key"
                  maxLength={64}
                  className="px-2 py-1 text-xs border border-gray-300 rounded font-mono"
                />
                =
                <input
                  aria-label={`Step ${i + 1} property ${k + 1} value`}
                  value={p.value}
                  onChange={(e) => update(i, (x) => ({ ...x, props: x.props.map((q, m) => (m === k ? { ...q, value: e.target.value } : q)) }))}
                  placeholder="value"
                  maxLength={256}
                  className="px-2 py-1 text-xs border border-gray-300 rounded font-mono"
                />
                <button type="button" onClick={() => update(i, (x) => ({ ...x, props: x.props.filter((_, m) => m !== k) }))} className="text-xs text-red-600">
                  ×
                </button>
              </div>
            ))}
          </div>
        ))}
        <div className="flex flex-wrap items-center gap-4 text-sm">
          <button type="button" onClick={() => setDraft((prev) => [...prev, emptyStep()])} disabled={draft.length >= maxSteps} className="text-blue-600 disabled:opacity-30">
            + step
          </button>
          <label className="flex items-center gap-2 text-gray-500">
            Within
            <input
              aria-label="Window"
              type="number"
              min={1}
              value={windowValue}
              onChange={(e) => setWindowValue(Number(e.target.value))}
              className="w-20 px-2 py-1 border border-gray-300 rounded text-gray-900"
            />
            <select aria-label="Window unit" value={windowUnit} onChange={(e) => setWindowUnit(e.target.value as 'm' | 'h')} className="px-2 py-1 border border-gray-300 rounded text-gray-900">
              <option value="m">minutes</option>
              <option value="h">hours</option>
            </select>
            of the first step
          </label>
          <label className="flex items-center gap-2 text-gray-500">
            Split by:
            <select aria-label="Split by" value={by} onChange={(e) => setBy(e.target.value as FunnelSplit)} className="px-2 py-1 border border-gray-300 rounded text-gray-900">
              <option value="">(none)</option>
              <option value="app_version">Version</option>
              <option value="build_type">Build type</option>
              <option value="platform">Platform</option>
            </select>
          </label>
          <button type="submit" className="px-4 py-1.5 bg-blue-600 text-white rounded">
            Run
          </button>
          {isFetching && <span className="text-gray-500">Running...</span>}
        </div>
        {error && <p className="text-sm text-red-600">{error}</p>}
        <p className="text-xs text-gray-500">
          A session reaches a step when its custom events contain the steps in this order within the window. A step with property
          filters only matches events sent individually, not the counts the SDK aggregates beyond its per-minute budget (which carry
          no properties). The range may be at most 31 days.
        </p>
      </form>

      {!request ? (
        <div className="bg-white rounded-lg shadow p-6 text-center text-gray-500 text-sm">Define the steps and run the funnel.</div>
      ) : isLoading ? (
        <div className="text-center py-8 text-gray-500">Loading...</div>
      ) : isError ? (
        <div className="text-center py-8 text-red-500">{requestError(queryError, 'Could not compute the funnel.')}</div>
      ) : (data?.groups ?? []).length === 0 ? (
        <div className="bg-white rounded-lg shadow p-6 text-center text-gray-500 text-sm">No session reached the first step.</div>
      ) : (
        <div className="space-y-4">
          {(data?.groups ?? []).map((g) => {
            const first = g.steps[0]?.sessions ?? 0
            return (
              <div key={g.value} className="bg-white rounded-lg shadow p-4" data-testid="funnel-group">
                {request.by && (
                  <h3 className="text-sm font-medium text-gray-700 mb-2 font-mono">
                    <Clipped text={g.value} />
                  </h3>
                )}
                <table className="min-w-full text-sm">
                  <thead className="text-gray-500 text-xs">
                    <tr>
                      <th className="text-left py-1">Step</th>
                      <th className="text-left py-1 w-1/3" />
                      <th className="text-right py-1">Sessions</th>
                      <th className="text-right py-1">From first</th>
                      <th className="text-right py-1">From previous</th>
                    </tr>
                  </thead>
                  <tbody>
                    {g.steps.map((s, i) => (
                      <tr key={i} className="border-t border-gray-100">
                        <td className="py-1 font-mono break-all">
                          {i + 1}. <Clipped text={s.name} />
                          {s.props && <span className="text-xs text-gray-500"> ({<Clipped text={propsText(s.props)} />})</span>}
                        </td>
                        <td className="py-1">
                          <div className="h-3 bg-gray-100 rounded">
                            <div className="h-3 bg-blue-500 rounded" style={{ width: `${first ? (s.sessions / first) * 100 : 0}%` }} />
                          </div>
                        </td>
                        <td className="py-1 text-right">{formatNumber(s.sessions)}</td>
                        <td className="py-1 text-right">{formatPercent(s.conversion ?? 0)}</td>
                        <td className="py-1 text-right">{i === 0 ? '' : formatPercent(s.from_previous ?? 0)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          })}
          {data?.truncated && <p className="text-xs text-gray-500">Showing the 20 groups with the most sessions at the first step.</p>}
        </div>
      )}
    </div>
  )
}
