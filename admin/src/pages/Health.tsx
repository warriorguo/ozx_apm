import { useState } from 'react'
import { GlobalFilterBar } from '../components/FilterBar'
import { Clipped, GroupPicker, groupLabel } from '../components/GroupPicker'
import { useFilters } from '../hooks/useFilters'
import { useSessionHealth } from '../hooks/useApi'
import { formatNumber, formatPercent, requestError } from '../utils/format'
import type { AnalysisGroup, CrashTypeName, SessionHealthRow } from '../types/api'

const crashTypes: { value: CrashTypeName; label: string; hint: string }[] = [
  { value: 'native', label: 'Native', hint: 'Native crash (signal)' },
  { value: 'java', label: 'Java', hint: 'Java crash' },
  { value: 'oom', label: 'OOM', hint: 'Killed for low memory in the foreground' },
  { value: 'killed', label: 'Killed', hint: 'Other foreground kill' },
  { value: 'unknown', label: 'Unknown', hint: 'Ended in the foreground without an exit record (Android 10 and older): may be the player' },
  { value: 'anr', label: 'ANR', hint: 'Application not responding (also shown as the ANR rate)' },
]

export const defaultCrashTypes: CrashTypeName[] = ['native', 'java', 'oom', 'killed']

export function rowKey(r: SessionHealthRow): string {
  return `${r.value}\u0000${r.build_type}`
}

// Difference of two fractions in percentage points, signed, 2 decimals.
export function deltaPP(value: number, base: number): string {
  const d = (value - base) * 100
  const s = d.toFixed(2)
  return (d > 0 ? '+' : d < 0 ? '' : '±') + s + ' pp'
}

function Rate({ value, base, higherIsBetter, testId }: { value: number; base?: number; higherIsBetter: boolean; testId?: string }) {
  let delta = null
  if (base !== undefined) {
    const d = value - base
    const good = higherIsBetter ? d > 0 : d < 0
    const bad = higherIsBetter ? d < 0 : d > 0
    delta = (
      <div className={`text-xs ${good ? 'text-green-600' : bad ? 'text-red-600' : 'text-gray-400'}`} data-testid={testId}>
        {deltaPP(value, base)}
      </div>
    )
  }
  return (
    <>
      {formatPercent(value ?? 0)}
      {delta}
    </>
  )
}

// Session health (APM-9): crash-free sessions and users, the ANR rate and
// exception rates per version and build type, or per value of a game context
// key (e.g. the data pack version), with deltas against a chosen baseline.
export function Health() {
  const { queryParams } = useFilters()
  const [group, setGroup] = useState<AnalysisGroup>({ kind: 'by', key: 'app_version' })
  const [types, setTypes] = useState<CrashTypeName[]>(defaultCrashTypes)
  const [baseline, setBaseline] = useState('')
  // Force-stops by the player (killed, USER_REQUESTED / USER_STOPPED) are not crashes unless asked for.
  const [userKills, setUserKills] = useState(false)
  const healthParams = userKills ? ({ ...queryParams, user_kills: '1' } as typeof queryParams) : queryParams
  const { data, isLoading, isError, error } = useSessionHealth(group, types, healthParams)
  const rows = data?.rows ?? []
  const base = rows.find((r) => rowKey(r) === baseline)

  const toggle = (t: CrashTypeName) =>
    setTypes((prev) => (prev.includes(t) ? (prev.length === 1 ? prev : prev.filter((x) => x !== t)) : [...prev, t].sort((a, b) => crashTypes.findIndex((c) => c.value === a) - crashTypes.findIndex((c) => c.value === b))))

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Session health</h1>
      <GlobalFilterBar />
      <div className="bg-white rounded-lg shadow p-4 mb-6 flex flex-wrap items-center gap-4">
        <GroupPicker value={group} onChange={setGroup} columns={['app_version', 'build_type', 'platform', 'device_model', 'ram_class']} params={queryParams} />
        <div className="flex items-center gap-3 text-sm text-gray-500">
          Counts as a crash:
          {crashTypes.map((c) => (
            <label key={c.value} className="flex items-center gap-1 text-gray-700" title={c.hint}>
              <input
                type="checkbox"
                checked={types.includes(c.value)}
                // At least one type: with none the server would count its defaults.
                disabled={types.length === 1 && types.includes(c.value)}
                onChange={() => toggle(c.value)}
                aria-label={`Count ${c.label}`}
              />
              {c.label}
            </label>
          ))}
          <label className="flex items-center gap-1 text-gray-700" title="Killed with Android's exit reason USER_REQUESTED or USER_STOPPED (force-stop): not a crash for Play Console either">
            <input type="checkbox" checked={userKills} onChange={() => setUserKills((v) => !v)} aria-label="Count force-stops" />
            Force-stops
          </label>
        </div>
        <label className="flex items-center gap-2 text-sm text-gray-500">
          Compare to:
          <select aria-label="Compare to" value={baseline} onChange={(e) => setBaseline(e.target.value)} className="px-3 py-1.5 text-sm border border-gray-300 rounded text-gray-900">
            <option value="">(none)</option>
            {rows.map((r) => (
              <option key={rowKey(r)} value={rowKey(r)}>
                {(r.value || '(not set)').slice(0, 60)} · {r.build_type || 'unknown'}
              </option>
            ))}
          </select>
        </label>
      </div>
      <p className="text-xs text-gray-500 mb-3">
        A session counts if any of its events is in the range; it is crashed if it has a crash of a checked type. ANRs are counted
        separately (like Play Console's ANR rate). Crashes reach the server at the next launch. Build type &quot;release&quot; includes
        events of SDKs without a build type. The range may be at most 31 days.
      </p>
      {isLoading ? (
        <div className="text-center py-8 text-gray-500">Loading...</div>
      ) : isError ? (
        <div className="text-center py-8 text-red-500">{requestError(error, 'Could not load the session health.')}</div>
      ) : rows.length === 0 ? (
        <div className="bg-white rounded-lg shadow p-6 text-center text-gray-500 text-sm">No sessions in this range.</div>
      ) : (
        <div className="bg-white rounded-lg shadow overflow-x-auto">
          <table className="min-w-full text-sm">
            <thead className="bg-gray-50 text-gray-500 text-xs">
              <tr>
                <th className="px-3 py-2 text-left">{groupLabel(group)}</th>
                <th className="px-3 py-2 text-left">Build</th>
                <th className="px-3 py-2 text-right">Sessions</th>
                <th className="px-3 py-2 text-right">Users</th>
                <th className="px-3 py-2 text-right">Crash-free sessions</th>
                <th className="px-3 py-2 text-right">Crash-free users</th>
                <th className="px-3 py-2 text-right">ANR rate</th>
                <th className="px-3 py-2 text-right">Sessions with exceptions</th>
                <th className="px-3 py-2 text-right">Exceptions / session</th>
                <th className="px-3 py-2 text-left">Crashes by type</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const k = rowKey(r)
                const b = base && rowKey(base) !== k ? base : undefined
                const byType = Object.entries(r.crashes_by_type ?? {}).filter(([, n]) => n > 0)
                return (
                  <tr key={k} className={`border-t border-gray-100 ${baseline === k ? 'bg-blue-50' : ''}`}>
                    <td className="px-3 py-2 font-mono break-all">
                      <Clipped text={r.value} />
                    </td>
                    <td className="px-3 py-2">{r.build_type || 'unknown'}</td>
                    <td className="px-3 py-2 text-right">
                      {formatNumber(r.sessions)}
                      <div className="text-xs text-gray-400">{formatNumber(r.crashed_sessions)} crashed</div>
                    </td>
                    <td className="px-3 py-2 text-right">
                      {formatNumber(r.users)}
                      <div className="text-xs text-gray-400">{formatNumber(r.crashed_users)} crashed</div>
                    </td>
                    <td className="px-3 py-2 text-right">
                      <Rate value={r.crash_free_sessions} base={b?.crash_free_sessions} higherIsBetter testId={`delta-cfs-${r.value}`} />
                    </td>
                    <td className="px-3 py-2 text-right">
                      <Rate value={r.crash_free_users} base={b?.crash_free_users} higherIsBetter />
                    </td>
                    <td className="px-3 py-2 text-right">
                      <Rate value={r.anr_rate} base={b?.anr_rate} higherIsBetter={false} testId={`delta-anr-${r.value}`} />
                    </td>
                    <td className="px-3 py-2 text-right">
                      <Rate value={r.exception_session_rate} base={b?.exception_session_rate} higherIsBetter={false} />
                    </td>
                    <td className="px-3 py-2 text-right">{(r.exceptions_per_session ?? 0).toFixed(2)}</td>
                    <td className="px-3 py-2 text-xs text-gray-600">
                      {byType.length === 0 ? '-' : byType.map(([t, n]) => `${t} ${n}`).join(' · ')}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
          {data?.truncated && <p className="px-4 py-2 text-xs text-gray-500">Showing the {rows.length} groups with the most sessions.</p>}
        </div>
      )}
    </div>
  )
}
