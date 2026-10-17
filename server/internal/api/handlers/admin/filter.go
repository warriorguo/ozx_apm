package admin

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// parseAdminFilter reads the filter parameters every admin endpoint accepts:
//
//	start_time, end_time  RFC 3339; default: the last defaultRange (an
//	                      unparsable value is ignored, as before APM-5)
//	app_version, platform exact match
//	build_type            release (default; includes events without a build
//	                      type from SDKs before APM-5), dev, editor,
//	                      unknown (only those), all
//	ctx                   key:value, repeatable. The same key repeated is an
//	                      OR, different keys are ANDed. "key:" matches events
//	                      without the key. At most models.MaxContextFilters.
//
// Context keys and values are only ever bound as query arguments.
func parseAdminFilter(q url.Values, defaultRange time.Duration) (models.AdminFilter, error) {
	f := models.AdminFilter{EndTime: time.Now()}
	f.StartTime = f.EndTime.Add(-defaultRange)
	if start := q.Get("start_time"); start != "" {
		if t, err := time.Parse(time.RFC3339, start); err == nil {
			f.StartTime = t
		}
	}
	if end := q.Get("end_time"); end != "" {
		if t, err := time.Parse(time.RFC3339, end); err == nil {
			f.EndTime = t
		}
	}
	f.AppVersion = q.Get("app_version")
	f.Platform = q.Get("platform")

	switch bt := q.Get("build_type"); bt {
	case "":
		f.BuildType = models.BuildFilterRelease
	case models.BuildFilterRelease, models.BuildFilterDev, models.BuildFilterEditor, models.BuildFilterUnknown, models.BuildFilterAll:
		f.BuildType = bt
	default:
		return f, fmt.Errorf("build_type must be one of release, dev, editor, unknown, all")
	}

	ctxParams := q["ctx"]
	if len(ctxParams) > models.MaxContextFilters {
		return f, fmt.Errorf("at most %d ctx filters", models.MaxContextFilters)
	}
	byKey := map[string]int{}
	for _, p := range ctxParams {
		key, value, ok := strings.Cut(p, ":")
		if !ok || !models.ValidContextKey(key) {
			return f, fmt.Errorf("ctx filter %q: want key:value with a key of [A-Za-z0-9_.-]{1,64}", p)
		}
		if utf8.RuneCountInString(value) > models.MaxContextValueLength {
			return f, fmt.Errorf("ctx filter %q: value longer than %d characters", key, models.MaxContextValueLength)
		}
		if i, seen := byKey[key]; seen {
			f.Context[i].Values = append(f.Context[i].Values, value)
			continue
		}
		byKey[key] = len(f.Context)
		f.Context = append(f.Context, models.ContextFilter{Key: key, Values: []string{value}})
	}
	return f, nil
}
