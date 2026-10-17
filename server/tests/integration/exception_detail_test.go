package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// APM-11 end to end: the exception detail page's endpoints (occurrences page
// by page, distribution by column and by context key, trend) and the list's
// sorts and first-error filter, through ingest -> ClickHouse -> admin API.

func TestExceptionDetail_DistributionTrendOccurrencesAndSorts(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupAPM4(t, async)
			awake := map[string]interface{}{"fingerprint": "client-awake", "log_type": "exception",
				"message": "InvalidOperationException: SaveManager not ready", "stack": "Ozx.Save.SaveManager.Awake () (at Assets/Scripts/Save/SaveManager.cs:31)\n",
				"error_seq": 1, "session_ms": 812}
			// The bus exception in three rooms (verification 4): 1+40, 25, 10 occurrences
			// and a merged count of 7 across rooms; a dev build's occurrence; and an
			// Awake root cause followed by the NRE cascade (verification 5).
			e.post(t,
				e.event("A", "r-1", awake),
				e.nre("A", "r-1", map[string]interface{}{"error_seq": 2, "session_ms": 830}),
				e.nre("A", "r-1", map[string]interface{}{"error_seq": 3, "session_ms": 846, "count": 40}),
				e.nre("A", "r-2", map[string]interface{}{"error_seq": 43, "session_ms": 2100, "count": 25}),
				e.nre("A", "r-3", map[string]interface{}{"error_seq": 68, "session_ms": 3300, "count": 10}),
				e.nre("A", "", map[string]interface{}{"error_seq": 78, "session_ms": 4000, "count": 7, "ctx_merged": true,
					"ctx": map[string]string{"levelId": "3"}}),
				e.nre("D", "r-1", map[string]interface{}{"error_seq": 1, "session_ms": 500, "build_type": "dev"}),
			)
			params := url.Values{"start_time": {time.UnixMilli(e.ts - 3600_000).UTC().Format(time.RFC3339)}}

			groups := groupsByType(t, e, params)
			nre := groups["System.NullReferenceException"]
			if nre.Count != 1+40+25+10+7 || nre.SessionCount != 1 || nre.FirstErrorSessions != 0 || nre.SessionsWithSeq != 1 {
				t.Fatalf("NRE group: %+v", nre)
			}

			// Verification 5: first errors only, the Awake failure on top.
			var list models.ExceptionListResponse
			getAdmin(t, e.admin, "/api/exceptions", withParams(params, "first_error", "1"), &list)
			if len(list.Exceptions) != 1 || list.Exceptions[0].ExceptionType != "InvalidOperationException" || list.Exceptions[0].Count != 1 {
				t.Errorf("first errors: %+v", list.Exceptions)
			}
			// Sorts: by count the NRE first; by first seen (newest first) also the
			// NRE (sent after the Awake failure); by sessions a tie broken by fingerprint.
			order := func(sort string) []string {
				var l models.ExceptionListResponse
				getAdmin(t, e.admin, "/api/exceptions", withParams(params, "sort", sort), &l)
				var types []string
				for _, g := range l.Exceptions {
					types = append(types, g.ExceptionType)
				}
				return types
			}
			if got := order("count"); len(got) != 2 || got[0] != "System.NullReferenceException" {
				t.Errorf("by count: %v", got)
			}
			if got := order("first_seen"); len(got) != 2 || got[0] != "System.NullReferenceException" {
				t.Errorf("by first seen: %v", got)
			}
			if got := order("last_seen"); len(got) != 2 || got[0] != "System.NullReferenceException" {
				t.Errorf("by last seen: %v", got)
			}
			if code := adminStatus(t, e.admin, "/api/exceptions", withParams(params, "sort", "message")); code != http.StatusBadRequest {
				t.Errorf("bad sort: %d", code)
			}

			// Verification 4: by roomId, 3 rooms, counts weighted by count; the
			// merged count apart.
			var dist models.ExceptionDistribution
			getAdmin(t, e.admin, "/api/exceptions/distribution", withParams(params, "fingerprint", nre.Fingerprint, "key", "roomId"), &dist)
			rooms := map[string]int64{}
			for _, r := range dist.Rows {
				rooms[r.Value] = r.Count
			}
			if dist.Total != 83 || dist.Merged != 7 || len(rooms) != 3 || rooms["r-1"] != 41 || rooms["r-2"] != 25 || rooms["r-3"] != 10 || dist.GroupBy != "ctx:roomId" {
				t.Errorf("by roomId: %+v", dist)
			}
			// By build type: release only by default; all builds shows the dev one.
			getAdmin(t, e.admin, "/api/exceptions/distribution", withParams(params, "fingerprint", nre.Fingerprint, "by", "build_type", "build_type", "all"), &dist)
			builds := map[string]int64{}
			for _, r := range dist.Rows {
				builds[r.Value] = r.Count
			}
			if dist.Total != 84 || builds["release"] != 83 || builds["dev"] != 1 || dist.Merged != 0 {
				t.Errorf("by build type: %+v", dist)
			}
			for _, by := range []string{"app_version", "device_model", "platform", "os_version"} {
				getAdmin(t, e.admin, "/api/exceptions/distribution", withParams(params, "fingerprint", nre.Fingerprint, "by", by), &dist)
				if len(dist.Rows) != 1 || dist.Rows[0].Count != 83 || dist.Rows[0].Sessions != 1 {
					t.Errorf("by %s: %+v", by, dist)
				}
			}

			// Detail: trend adds up to the count; first/last version.
			var d models.ExceptionDetail
			getAdmin(t, e.admin, "/api/exceptions/detail", withParams(params, "fingerprint", nre.Fingerprint), &d)
			var sum int64
			for _, p := range d.Trend.Points {
				sum += p.Count
			}
			if sum != 83 || d.Trend.IntervalSeconds != 60 || d.FirstVersion != "2.1" || d.SessionsWithSeq != 1 || d.Occurrences[0].DeviceID != "d-A" {
				t.Errorf("detail: trend %+v, sum %d, %+v", d.Trend, sum, d.ExceptionGroup)
			}

			// Occurrences page by page, most recent first, nothing twice.
			seen := map[string]bool{}
			for page := 1; page <= 3; page++ {
				var occ models.ExceptionOccurrencesResponse
				getAdmin(t, e.admin, "/api/exceptions/occurrences", withParams(params, "fingerprint", nre.Fingerprint, "page_size", "2", "page", fmt.Sprint(page)), &occ)
				if occ.TotalCount != 5 || (page < 3 && len(occ.Occurrences) != 2) || (page == 3 && len(occ.Occurrences) != 1) {
					t.Fatalf("page %d: %+v", page, occ)
				}
				for _, o := range occ.Occurrences {
					if seen[o.EventID] {
						t.Errorf("%s twice", o.EventID)
					}
					seen[o.EventID] = true
				}
			}
			var empty models.ExceptionOccurrencesResponse
			getAdmin(t, e.admin, "/api/exceptions/occurrences", withParams(params, "fingerprint", nre.Fingerprint, "page_size", "2", "page", "9"), &empty)
			if empty.TotalCount != 5 || len(empty.Occurrences) != 0 || empty.Occurrences == nil {
				t.Errorf("past the end: %+v", empty)
			}
			// A context filter narrows the occurrences.
			getAdmin(t, e.admin, "/api/exceptions/occurrences", withParams(params, "fingerprint", nre.Fingerprint, "ctx", "roomId:r-3"), &empty)
			if empty.TotalCount != 1 || empty.Occurrences[0].Count != 10 || empty.Occurrences[0].Context["roomId"] != "r-3" {
				t.Errorf("roomId r-3: %+v", empty)
			}
		})
	}
}
