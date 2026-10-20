# OZX operations data: tables and how to query them

For designers and operations writing their own questions in Metabase (or any
SQL client) on the operations ("ops") data of APM-12. The preset dashboards
(Players, Funnels, Ads) are built from the same tables; their SQL is in
`server/internal/opsbi/cards.go`, a good place to copy from.

- Database `ozx_apm` on ClickHouse, read with the BI account `ozx_ops_ro`
  (it can only read the five tables below; a query may run 60 s, read 2 billion
  rows and use 2 GB of memory).
- One row per event the game sends with `ApmClient.OpsEvent` / `ApmAds`, plus
  the SDK's own session events. Only with the player's analytics consent.
- Kept **one year** (the per-player tables), then deleted automatically.
  A player can ask to be deleted (by player id): their rows disappear.
- No device id, device model, IP address, location, stack or log text is stored
  here. Never put personal data (names, e-mail, chat text) into event
  properties: the game must not send it.

## The rules that keep numbers right

1. **Always read `ops_events` with `FINAL`** (`FROM ops_events FINAL`), or count
   distinct ids (`uniqExact(event_id)`). An event can reach the server more than
   once (offline retries, lost answers); the server drops repeats, and `FINAL`
   removes the rare one that slips through.
2. **Days are UTC days.** Group by the `day` column (or `toDate(event_time)`),
   never by `toDate(client_time)`. "Today" is `toDate(now('UTC'))`, not
   `today()` (the server's time zone).
3. **A player is `player_id`**, not a device and not a session. A reinstall (or
   clearing the app's data) is a new player.
4. **Filter build types**: `build_type = 'release'` for real players (`dev` and
   `editor` are test builds).
5. Count players with `uniqExact(player_id)`; never `count()` of rows of
   `ops_player_days` or `ops_players` without `FINAL` / `GROUP BY player_id`.

## Which day does an event count on?

Every event has three times (all UTC):

| column | what it is |
|---|---|
| `client_time` | the phone's clock when the event happened |
| `server_time` | when the server received it |
| `event_time` | the time it counts at: `client_time`, unless the phone's clock was evidently wrong |
| `day` | `toDate(event_time)`: the day every daily figure uses |

An event played offline and uploaded days later counts on **the day it was
played** (its `client_time`): a player who played on Monday without network
and connected on Wednesday is active on Monday, not Wednesday. `event_time`
falls back to `server_time` only when the phone's time cannot be right: more
than 5 minutes after the server received it (a clock set into the future; an
event is never received before it happened), or more than a year before (a
clock far in the past). A clock that is a few hours or days *behind* cannot be
told apart from offline play and counts on the earlier day.

`seq` is the player's install-wide sequence number of ops events (1, 2, 3...):
gaps mean events were lost on the device (only beyond a 4 MB backlog).

## Tables

### `ops_events`: one row per event

| column | type | meaning |
|---|---|---|
| `event_id` | String | unique id of the event (from the SDK) |
| `name` | LowCardinality(String) | the event name (`run_start`, `ad_show`, ...) |
| `day` | Date | UTC day it counts on (see above) |
| `event_time` | DateTime64(3) | time it counts at (UTC) |
| `client_time` | DateTime64(3) | the phone's time (UTC); 1970-01-01 if it sent none |
| `server_time` | DateTime64(3) | received at (UTC) |
| `seq` | UInt64 | sequence number of the player's ops events |
| `player_id` | String | anonymous player id |
| `session_id` | String | the SDK session (a launch, or a return after 30 s in the background) |
| `app_version` | LowCardinality(String) | game version |
| `platform` | LowCardinality(String) | `Android`, `iOS`, ... |
| `build_type` | LowCardinality(String) | `release`, `dev`, `editor`, `''` (unknown) |
| `channel` | LowCardinality(String) | the store the game was installed from (`googleplay`, ...; `''` unknown) |
| `country` | LowCardinality(String) | ISO country code (`DE`, `BR`, ...; `''` unknown): from the CDN when configured, else the phone's region setting. Never a location. |
| `props` | Map(String, String) | the event's properties, as text: `props['placement']` |
| `prop_types` | Map(String, UInt8) | which properties were numbers (1) or booleans (2) |
| `num_props` | Map(String, Float64) | the numeric properties as numbers: `num_props['revenue']` |
| `ctx` | Map(String, String) | the game context when the event happened, whitelisted keys only: `ctx['chapter']` |

A missing key reads as `''` in `props`/`ctx` and as `0` in `num_props`; test
with `mapContains(props, 'placement')` when that matters.

Sorted by `(name, day, event_id)`: filtering on `name` and `day` is fast.

### `ops_player_days`: one row per player and active day

`day, player_id, app_version, platform, build_type, channel, country`. A player
is active on a day if they sent any ops event that day (every launch sends
`apm_session_start`). The other columns are one of that day's events' values.
Read with `uniqExact(player_id)` per `day`, or `FINAL`.

### `ops_players`: one row per player

| column | meaning |
|---|---|
| `player_id` | |
| `first_seen` | the player's first ops event (UTC); `toDate(toTimeZone(first_seen, 'UTC'))` is their first day |
| `last_seen` | their latest activity (updated about once a day) |
| `install_channel` | the channel of their events |
| `platform`, `build_type` | `release` once any release build sent an event |

Partial rows are merged in the background: always
`SELECT player_id, min(first_seen), max(last_seen), max(install_channel) ... GROUP BY player_id`
(or `FROM ops_players FINAL`).

### `ops_fx_rates`: daily exchange rates (imported)

`day, currency, usd_per_unit, source, imported_at`: US dollars per unit of the
currency on that day. Read with `FINAL` (a re-import replaces a day). The
revenue questions use the latest rate on or before the impression's day:

```sql
SELECT e.day, e.rev * if(e.cur IN ('USD', ''), 1, r.rate) AS revenue_usd
FROM (SELECT day, num_props['revenue'] AS rev, upper(props['currency']) AS cur
      FROM ops_events FINAL WHERE name = 'ad_show') AS e
ASOF LEFT JOIN (SELECT toString(currency) AS cur, day, usd_per_unit AS rate FROM ops_fx_rates FINAL) AS r
ON e.cur = r.cur AND e.day >= r.day
```

### `ops_ad_reports`: the ad networks' own daily reports (imported)

`day, network, country, placement, ad_unit, impressions, revenue, currency, source, imported_at`,
one row per day, network, country and placement (or ad unit); read with `FINAL`.
`network` is lower case, `country` an ISO code (`''` if the report had none).

## Event catalog

### Sent by the SDK itself

| name | when | properties |
|---|---|---|
| `apm_session_start` | at every launch and every return after 30 s in the background (with analytics consent; also when the player grants it during a session) | none |
| `apm_session_end` | when the game goes to the background or quits (not when it is killed) | `foreground_ms` (number: time in the foreground in this session so far), `reason` (`pause`, `quit`, `reset`) |

Session length = `greatest(max(foreground_ms of apm_session_end) / 1000, last event - first event)` per `session_id`.

### Ads (ApmAds; rewarded video, OZX-1280)

| name | step | properties |
|---|---|---|
| `ad_entry_show` | the offer (button, popup) became visible | `placement` |
| `ad_click` | the player chose to watch | `placement` |
| `ad_request` | an ad was requested | `placement`, `network` |
| `ad_show` | impression | `placement`, `network`, `revenue` (number, in `currency`; an estimate by the ad SDK), `currency` (`USD`, ...), `precision`, `ad_type` |
| `ad_complete` | watched to the end | `placement`, `network` |
| `ad_reward` | reward granted | `placement`, `network` |
| `ad_fail` | load or show failed | `placement`, `network`, `reason` |

`network` is lower case (`admob`, `applovin`, ...), as in the imported reports.
The run's context comes with each ad event in `ctx`: `ctx['chapter']`,
`ctx['room']`, `ctx['revivesUsed']`.

### Game events (defined by OZX)

Every other name is the game's own (`run_start`, `run_end`, `tutorial_1`, ...).
The game decides names and properties (OZX's event list); a name is 1-64
characters from `[A-Za-z0-9_.-]`. Look at what exists:

```sql
SELECT name, count() AS events, uniqExact(player_id) AS players, groupUniqArrayArray(mapKeys(props)) AS prop_keys
FROM ops_events FINAL WHERE day >= toDate(now('UTC')) - 7 GROUP BY name ORDER BY events DESC
```

### Context keys

Only these game context keys are kept (server setting `ops.context_keys`):
`runId`, `chapter`, `levelId`, `room`, `roomId`, `revivesUsed`. Ask the APM
admins to add one.

## Recipes

Daily active players (release builds, last 30 days):

```sql
SELECT day, uniqExact(player_id) AS dau
FROM ops_player_days
WHERE day >= toDate(now('UTC')) - 30 AND build_type = 'release'
GROUP BY day ORDER BY day
```

New players per day:

```sql
SELECT first_day, count() AS new_players
FROM (
    SELECT player_id, toDate(toTimeZone(min(first_seen), 'UTC')) AS first_day
    FROM ops_players
    GROUP BY player_id
    HAVING max(build_type) = 'release'
)
WHERE first_day >= toDate(now('UTC')) - 30
GROUP BY first_day ORDER BY first_day
```

D1 / D7 retention of each day's new players: card "Retention D1 / D7 / D30 by first day".

A property breakdown (e.g. run results by chapter):

```sql
SELECT ctx['chapter'] AS chapter, props['result'] AS result, count() AS runs, uniqExact(player_id) AS players
FROM ops_events FINAL
WHERE name = 'run_end' AND day >= toDate(now('UTC')) - 7 AND build_type = 'release'
GROUP BY chapter, result ORDER BY chapter, runs DESC
```

A funnel of your own (players who did A, then B, then C within an hour):

```sql
SELECT countIf(level >= 1) AS a, countIf(level >= 2) AS b, countIf(level >= 3) AS c
FROM (
    SELECT player_id, windowFunnel(3600)(toDateTime(event_time), name = 'tutorial_1', name = 'tutorial_2', name = 'tutorial_3') AS level
    FROM ops_events FINAL
    WHERE day >= toDate(now('UTC')) - 7 AND name IN ('tutorial_1', 'tutorial_2', 'tutorial_3')
    GROUP BY player_id
)
```

A number property: `sum(num_props['revenue'])`, `avg(num_props['duration_s'])`,
`quantileExact(0.5)(num_props['duration_s'])`.

## Importing rates and network reports (operations)

With the import token (ask the APM admins):

```sh
export OZX_ADMIN_OPS_TOKEN=...      # never on the command line
go run ./cmd/opsimport fx-rates  -url https://apm-admin.example.com rates.csv
go run ./cmd/opsimport ad-report -url https://apm-admin.example.com -network admob \
   -map 'date=Date,country=Country,ad_unit=Ad unit,impressions=Impressions,revenue=Estimated earnings (USD)' admob.csv
```

- Rates file: `date,currency,usd_per_unit` (header line; `2026-10-19,EUR,1.0712`):
  US dollars per unit, e.g. from a central bank's daily reference rates. One file
  may hold many days; missing days (weekends) use the last earlier rate.
- Report file: `date,network,country,placement,impressions,revenue,currency`
  (`network`, `placement`, `ad_unit`, `currency` optional: `-network`, default USD);
  any other header names through `-map`. Numbers with `.` as decimal separator.
- `-dry-run` checks a file without storing it. A file with one bad line is refused
  as a whole (the message names the line). Importing the same day again replaces it.
