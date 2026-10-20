package handlers

import (
	"context"
	"sync"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Ops events (APM-12).
//
// An ops event is a custom event with "ops": true. Ingest normalizes it
// (processor.OpsNormalizer: whitelisted context, coarse country, the time
// rules), drops the ones already stored (by event_id, like APM-19's lookup;
// a re-sent event has the same name, version and client time), inserts the
// rest into ops_events, and writes the derived rows that make DAU, new
// players and retention cheap: ops_player_days (player active on a day) and
// ops_players (first and last seen, install channel). The derived rows are
// written for every valid ops event of the request, duplicates included
// (they are idempotent: ReplacingMergeTree / AggregatingMergeTree), except
// those an in-process cache says were written already. If they fail, the
// request's ops events count as failed (503, the SDK resends: the events are
// then duplicates, the rows are written again).

// OpsStore is what the ingest handler needs for ops events. The repository
// implements it; a store without it refuses ops events (tests).
type OpsStore interface {
	InsertOpsEvents(ctx context.Context, events []models.OpsEvent) error
	InsertOpsPlayerDays(ctx context.Context, rows []models.OpsPlayerDay) error
	InsertOpsPlayers(ctx context.Context, rows []models.OpsPlayer) error
}

// opsSeen remembers which (player, day) rows this process has written, so a
// player's later requests that day do not write them again: per key, the
// earliest and latest event time written. A row is written again when an
// event is earlier than the earliest (an older event uploaded late: lowers
// first_seen) or more than opsLastSeenStep after the latest (keeps
// last_seen, which the TTL of ops_players follows, roughly current).
// Bounded: cleared when it holds opsSeenMax keys (the rows are idempotent,
// the cache only saves inserts). Safe for concurrent requests.
type opsSeen struct {
	mu   sync.Mutex
	keys map[opsSeenKey]opsSeenSpan
}

type opsSeenKey struct {
	player string
	day    int64 // Unix days
}

type opsSeenSpan struct{ min, max time.Time }

const (
	opsSeenMax      = 200000
	opsLastSeenStep = time.Hour
)

func newOpsSeen() *opsSeen { return &opsSeen{keys: map[opsSeenKey]opsSeenSpan{}} }

// opsRows builds the derived rows of the request's ops events that are not
// covered by the cache, and the cache entries to commit once they are written.
func (s *opsSeen) opsRows(events []models.OpsEvent) ([]models.OpsPlayerDay, []models.OpsPlayer, map[opsSeenKey]opsSeenSpan) {
	type group struct {
		day    models.OpsPlayerDay
		player models.OpsPlayer
	}
	groups := map[opsSeenKey]*group{}
	var order []opsSeenKey
	for i := range events {
		e := &events[i]
		k := opsSeenKey{e.PlayerID, e.Day.Unix() / 86400}
		g := groups[k]
		if g == nil {
			g = &group{
				day: models.OpsPlayerDay{Day: e.Day, PlayerID: e.PlayerID, AppVersion: e.AppVersion, Platform: e.Platform,
					BuildType: e.BuildType, Channel: e.Channel, Country: e.Country},
				player: models.OpsPlayer{PlayerID: e.PlayerID, FirstSeen: e.EventTime, LastSeen: e.EventTime},
			}
			groups[k] = g
			order = append(order, k)
		}
		p := &g.player
		if e.EventTime.Before(p.FirstSeen) {
			p.FirstSeen = e.EventTime
		}
		if e.EventTime.After(p.LastSeen) {
			p.LastSeen = e.EventTime
		}
		p.InstallChannel = maxString(p.InstallChannel, e.Channel)
		p.Platform = maxString(p.Platform, e.Platform)
		p.BuildType = maxString(p.BuildType, e.BuildType)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var days []models.OpsPlayerDay
	var players []models.OpsPlayer
	commit := map[opsSeenKey]opsSeenSpan{}
	for _, k := range order {
		g := groups[k]
		seen, ok := s.keys[k]
		if !ok {
			days = append(days, g.day)
		}
		if ok && !g.player.FirstSeen.Before(seen.min) && !g.player.LastSeen.After(seen.max.Add(opsLastSeenStep)) {
			continue
		}
		players = append(players, g.player)
		span := opsSeenSpan{g.player.FirstSeen, g.player.LastSeen}
		if ok {
			if seen.min.Before(span.min) {
				span.min = seen.min
			}
			if seen.max.After(span.max) {
				span.max = seen.max
			}
		}
		commit[k] = span
	}
	return days, players, commit
}

// remember commits the entries of rows that were written.
func (s *opsSeen) remember(commit map[opsSeenKey]opsSeenSpan) {
	if len(commit) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.keys)+len(commit) > opsSeenMax {
		s.keys = map[opsSeenKey]opsSeenSpan{}
	}
	for k, v := range commit {
		s.keys[k] = v
	}
}

func maxString(a, b string) string {
	if b > a {
		return b
	}
	return a
}
