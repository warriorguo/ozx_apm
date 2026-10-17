package admin

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

type fakeExceptionStore struct {
	filter         models.AdminFilter
	sort, fp       string
	page, pageSize int
	group          storage.CustomGroup
	detail         *models.ExceptionDetail
	err            error
}

func (f *fakeExceptionStore) GetExceptionGroups(_ context.Context, flt models.AdminFilter, sort string, page, pageSize int) ([]models.ExceptionGroup, int64, error) {
	f.filter, f.sort, f.page, f.pageSize = flt, sort, page, pageSize
	return []models.ExceptionGroup{}, 0, f.err
}

func (f *fakeExceptionStore) GetExceptionDetail(_ context.Context, fp string, flt models.AdminFilter) (*models.ExceptionDetail, error) {
	f.fp, f.filter = fp, flt
	return f.detail, f.err
}

func (f *fakeExceptionStore) GetExceptionOccurrences(_ context.Context, fp string, flt models.AdminFilter, page, pageSize int) (*models.ExceptionOccurrencesResponse, error) {
	f.fp, f.filter, f.page, f.pageSize = fp, flt, page, pageSize
	return &models.ExceptionOccurrencesResponse{Fingerprint: fp, Occurrences: []models.ExceptionOccurrence{}, Page: page, PageSize: pageSize}, f.err
}

func (f *fakeExceptionStore) GetExceptionDistribution(_ context.Context, flt models.AdminFilter, fp string, g storage.CustomGroup) (*models.ExceptionDistribution, error) {
	f.filter, f.fp, f.group = flt, fp, g
	return &models.ExceptionDistribution{Fingerprint: fp, GroupBy: g.String(), Rows: []models.ExceptionDistributionRow{}}, f.err
}

func TestExceptionHandler_ListSortAndFilters(t *testing.T) {
	store := &fakeExceptionStore{}
	h := &ExceptionHandler{repo: store, logger: zap.NewNop()}
	w := serve(h.ListExceptions, "/api/exceptions?sort=first_seen&first_error=1&ctx=levelId:3&page=2&page_size=50")
	if w.Code != 200 || store.sort != "first_seen" || !store.filter.FirstErrorOnly || len(store.filter.Context) != 1 || store.page != 2 || store.pageSize != 50 {
		t.Fatalf("%d %s %+v", w.Code, w.Body, store)
	}
	// Defaults: count, release builds, page 1 of 20.
	serve(h.ListExceptions, "/api/exceptions")
	if store.sort != "" || store.filter.BuildType != models.BuildFilterRelease || store.page != 1 || store.pageSize != 20 {
		t.Errorf("%+v", store)
	}
	if w := serve(h.ListExceptions, "/api/exceptions?sort=cnt%20DESC"); w.Code != http.StatusBadRequest {
		t.Errorf("bad sort: %d", w.Code)
	}
}

func TestExceptionHandler_Occurrences(t *testing.T) {
	store := &fakeExceptionStore{}
	h := &ExceptionHandler{repo: store, logger: zap.NewNop()}
	w := serve(h.GetOccurrences, "/api/exceptions/occurrences?fingerprint=fad7&page=3&page_size=5&build_type=all")
	if w.Code != 200 || store.fp != "fad7" || store.page != 3 || store.pageSize != 5 || store.filter.BuildType != "all" {
		t.Fatalf("%d %s %+v", w.Code, w.Body, store)
	}
	// page_size beyond 50 falls back to the default (10).
	serve(h.GetOccurrences, "/api/exceptions/occurrences?fingerprint=fad7&page_size=500")
	if store.pageSize != 10 || store.page != 1 {
		t.Errorf("%+v", store)
	}
	for _, target := range []string{"/api/exceptions/occurrences", "/api/exceptions/occurrences?fingerprint=f&build_type=x"} {
		if w := serve(h.GetOccurrences, target); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", target, w.Code)
		}
	}
	store.err = errors.New("boom")
	if w := serve(h.GetOccurrences, "/api/exceptions/occurrences?fingerprint=f"); w.Code != 500 {
		t.Errorf("%d", w.Code)
	}
}

func TestExceptionHandler_Distribution(t *testing.T) {
	store := &fakeExceptionStore{}
	h := &ExceptionHandler{repo: store, logger: zap.NewNop()}
	if w := serve(h.GetDistribution, "/api/exceptions/distribution?fingerprint=f"); w.Code != 200 || store.group != (storage.CustomGroup{Kind: "column", Key: "app_version"}) {
		t.Fatalf("%d %+v", w.Code, store.group)
	}
	if w := serve(h.GetDistribution, "/api/exceptions/distribution?fingerprint=f&key=roomId&first_error=1"); w.Code != 200 ||
		store.group != (storage.CustomGroup{Kind: "ctx", Key: "roomId"}) || !store.filter.FirstErrorOnly {
		t.Fatalf("%d %+v", w.Code, store.group)
	}
	for _, by := range []string{"build_type", "device_model", "platform", "os_version"} {
		if w := serve(h.GetDistribution, "/api/exceptions/distribution?fingerprint=f&by="+by); w.Code != 200 || store.group.Key != by {
			t.Errorf("%s: %d", by, w.Code)
		}
	}
	for _, q := range []string{"fingerprint=f&by=message", "fingerprint=f&key=bad%20key", "fingerprint=f&key=a&by=platform", "key=roomId", "fingerprint=f&prop=x&by=scene"} {
		if w := serve(h.GetDistribution, "/api/exceptions/distribution?"+q); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
}

func TestExceptionHandler_DetailNotFound(t *testing.T) {
	store := &fakeExceptionStore{}
	h := &ExceptionHandler{repo: store, logger: zap.NewNop()}
	if w := serve(h.GetExceptionDetail, "/api/exceptions/detail?fingerprint=f"); w.Code != http.StatusNotFound {
		t.Errorf("%d", w.Code)
	}
	store.detail = &models.ExceptionDetail{}
	if w := serve(h.GetExceptionDetail, "/api/exceptions/detail?fingerprint=f"); w.Code != 200 || store.filter.BuildType != models.BuildFilterRelease {
		t.Errorf("%d", w.Code)
	}
}

func TestExceptionHandler_NilRepoNewEndpoints(t *testing.T) {
	h := NewExceptionHandler(nil, zap.NewNop())
	for _, hf := range []http.HandlerFunc{h.GetOccurrences, h.GetDistribution} {
		if w := serve(hf, "/x?fingerprint=f"); w.Code != 500 {
			t.Errorf("%d", w.Code)
		}
	}
}
