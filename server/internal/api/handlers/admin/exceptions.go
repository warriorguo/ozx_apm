package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// ExceptionStore is the part of the repository the exception endpoints read.
type ExceptionStore interface {
	GetExceptionGroups(ctx context.Context, f models.AdminFilter, sort string, page, pageSize int) ([]models.ExceptionGroup, int64, error)
	GetExceptionDetail(ctx context.Context, fp string, f models.AdminFilter) (*models.ExceptionDetail, error)
	GetExceptionOccurrences(ctx context.Context, fp string, f models.AdminFilter, page, pageSize int) (*models.ExceptionOccurrencesResponse, error)
	GetExceptionDistribution(ctx context.Context, f models.AdminFilter, fp string, g storage.CustomGroup) (*models.ExceptionDistribution, error)
}

// ExceptionHandler serves the exception list (APM-4/APM-3) and the detail
// page's endpoints (APM-11). All take the common filter (parseAdminFilter).
type ExceptionHandler struct {
	repo   ExceptionStore
	logger *zap.Logger
}

func NewExceptionHandler(repo *storage.Repository, logger *zap.Logger) *ExceptionHandler {
	h := &ExceptionHandler{logger: logger}
	if repo != nil {
		h.repo = repo
	}
	return h
}

// exceptionDetailRange is the default range of the detail endpoints.
const exceptionDetailRange = 30 * 24 * time.Hour

// pageParams reads page (default 1) and page_size (default def, at most max);
// out-of-range values fall back to the defaults, as the list always did.
func pageParams(q url.Values, def, max int) (int, int) {
	page := 1
	if p := q.Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 && v <= 100000 {
			page = v
		}
	}
	size := def
	if ps := q.Get("page_size"); ps != "" {
		if v, err := strconv.Atoi(ps); err == nil && v > 0 && v <= max {
			size = v
		}
	}
	return page, size
}

// ListExceptions: GET /api/exceptions[?sort=count|sessions|first_seen|last_seen]
// -> one page of groups (default sort: count).
func (h *ExceptionHandler) ListExceptions(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	f, err := parseAdminFilter(q, 7*24*time.Hour)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sort := q.Get("sort")
	if !storage.ValidExceptionSort(sort) {
		http.Error(w, "sort must be one of count, sessions, first_seen, last_seen", http.StatusBadRequest)
		return
	}
	page, pageSize := pageParams(q, 20, 100)
	exceptions, totalCount, err := h.repo.GetExceptionGroups(r.Context(), f, sort, page, pageSize)
	if err != nil {
		h.logger.Error("failed to get exception groups", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, models.ExceptionListResponse{
		Exceptions: exceptions,
		TotalCount: totalCount,
		Page:       page,
		PageSize:   pageSize,
	})
}

// fingerprintAndFilter reads the required fingerprint and the filter (default
// range 30 days); false after writing a 400.
func fingerprintAndFilter(w http.ResponseWriter, q url.Values) (string, models.AdminFilter, bool) {
	fp := q.Get("fingerprint")
	if fp == "" || len(fp) > 256 {
		http.Error(w, "fingerprint parameter required", http.StatusBadRequest)
		return "", models.AdminFilter{}, false
	}
	f, err := parseAdminFilter(q, exceptionDetailRange)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", f, false
	}
	return fp, f, true
}

// GetExceptionDetail serves /api/exceptions/detail?fingerprint=F (APM-4;
// APM-11 adds the trend). Same filters as the list; default range 30 days;
// 404 when no row of the group matches.
func (h *ExceptionHandler) GetExceptionDetail(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}
	fp, f, ok := fingerprintAndFilter(w, r.URL.Query())
	if !ok {
		return
	}
	detail, err := h.repo.GetExceptionDetail(r.Context(), fp, f)
	if err != nil {
		h.logger.Error("failed to get exception detail", zap.Error(err), zap.String("fingerprint", fp))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if detail == nil {
		http.Error(w, "exception not found", http.StatusNotFound)
		return
	}
	writeJSON(w, detail)
}

// GetOccurrences serves /api/exceptions/occurrences?fingerprint=F[&page=N]
// [&page_size=M] (APM-11): the group's rows, most recent first, with context,
// breadcrumbs, session and device; page_size default 10, at most 50.
func (h *ExceptionHandler) GetOccurrences(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	fp, f, ok := fingerprintAndFilter(w, q)
	if !ok {
		return
	}
	page, pageSize := pageParams(q, 10, storage.MaxOccurrencesPageSize)
	if (page-1)*pageSize >= storage.MaxOccurrencesOffset {
		http.Error(w, fmt.Sprintf("only the %d most recent occurrences can be paged through; narrow the time range", storage.MaxOccurrencesOffset), http.StatusBadRequest)
		return
	}
	resp, err := h.repo.GetExceptionOccurrences(r.Context(), fp, f, page, pageSize)
	if err != nil {
		h.logger.Error("failed to get exception occurrences", zap.Error(err), zap.String("fingerprint", fp))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

// exceptionColumns are the columns a distribution of a group may use.
var exceptionColumns = "app_version, build_type, device_model, platform, os_version"

// GetDistribution serves /api/exceptions/distribution?fingerprint=F and one
// of by=app_version|build_type|device_model|platform|os_version (default
// app_version) or key=<context key> (APM-11): occurrences per value.
func (h *ExceptionHandler) GetDistribution(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	fp, f, ok := fingerprintAndFilter(w, q)
	if !ok {
		return
	}
	key, by := q.Get("key"), q.Get("by")
	var g storage.CustomGroup
	switch {
	case key != "" && by != "":
		http.Error(w, "give one of key, by", http.StatusBadRequest)
		return
	case key != "":
		if !models.ValidContextKey(key) {
			http.Error(w, "key: [A-Za-z0-9_.-]{1,64}", http.StatusBadRequest)
			return
		}
		g = storage.CustomGroup{Kind: "ctx", Key: key}
	default:
		if by == "" {
			by = "app_version"
		}
		if !storage.ValidCustomColumn(by) {
			http.Error(w, "by must be one of "+exceptionColumns, http.StatusBadRequest)
			return
		}
		g = storage.CustomGroup{Kind: "column", Key: by}
	}
	dist, err := h.repo.GetExceptionDistribution(r.Context(), f, fp, g)
	if err != nil {
		h.logger.Error("failed to get exception distribution", zap.Error(err), zap.String("fingerprint", fp), zap.String("group", g.String()))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, dist)
}
