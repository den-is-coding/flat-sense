package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Дополнительные тесты приёмки #57: разбор query-параметров,
// нормализация пагинации, 404 для несуществующего объявления.

func TestFiltersFromRequest(t *testing.T) {
	req := httptest.NewRequest("GET", "/admin/listings"+
		"?city=Москва&complex=ЖК+Север&q=студия&rooms=0&priceMin=7000000&priceMax=9000000"+
		"&hasCoords=0&sort=price&dir=ASC&limit=100&page=2", nil)
	f := filtersFromRequest(req)

	if f.City != "Москва" || f.Complex != "ЖК Север" || f.Search != "студия" {
		t.Fatalf("text filters: %+v", f)
	}
	// rooms=0 — это фильтр «студия», а не фильтр по числу комнат
	if f.Studio == nil || !*f.Studio {
		t.Fatalf("rooms=0 must map to studio filter, got %+v", f.Studio)
	}
	if f.Rooms != 0 {
		t.Fatalf("rooms must stay 0 for studio, got %d", f.Rooms)
	}
	if f.PriceMin != 7_000_000 || f.PriceMax != 9_000_000 {
		t.Fatalf("price range: %+v", f)
	}
	if f.HasCoords == nil || *f.HasCoords {
		t.Fatalf("hasCoords=0 must set false pointer, got %+v", f.HasCoords)
	}
	if f.SortBy != "price" || f.SortDir != "asc" {
		t.Fatalf("sort: %s/%s", f.SortBy, f.SortDir)
	}

	// явный фильтр «не студии» и «есть координаты»
	req2 := httptest.NewRequest("GET", "/admin/listings?studio=0&hasCoords=1", nil)
	f2 := filtersFromRequest(req2)
	if f2.Studio == nil || *f2.Studio {
		t.Fatalf("studio=0 must set false, got %+v", f2.Studio)
	}
	if f2.HasCoords == nil || !*f2.HasCoords {
		t.Fatalf("hasCoords=1 must set true, got %+v", f2.HasCoords)
	}
}

func TestFilters_NormalizeLimits(t *testing.T) {
	// лимит обрезается сверху, страница >= 1, сортировка — только белый список
	f := ListingFilters{Limit: 10_000, Page: -5, SortBy: "price; drop table", SortDir: "ASC"}
	listSQL, _, _ := f.buildQuery()
	if f.Limit != 500 {
		t.Fatalf("limit clamp: %d", f.Limit)
	}
	if f.Page != 1 {
		t.Fatalf("page clamp: %d", f.Page)
	}
	if !strings.Contains(listSQL, "ORDER BY last_seen_at DESC") {
		t.Fatalf("bad sort column must fall back to default: %s", listSQL)
	}
	// значения по умолчанию
	f2 := ListingFilters{}
	f2.buildQuery()
	if f2.Limit != 50 || f2.Page != 1 || f2.SortBy != "last_seen_at" || f2.SortDir != "desc" {
		t.Fatalf("defaults: %+v", f2)
	}
}

func TestAPIGet_NotFound(t *testing.T) {
	// store возвращает (nil, nil) — обработчик обязан отдать 404
	st := &nilStore{}
	srv := newTestServer(t, st)
	defer srv.Close()
	client := loginForm(t, srv)

	resp, err := client.Get(srv.URL + "/admin/listings/12345")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing listing: status = %d, want 404", resp.StatusCode)
	}

	// нечисловой id — 400
	resp2, err := client.Get(srv.URL + "/admin/listings/abc")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-numeric id: status = %d, want 400", resp2.StatusCode)
	}
}

func TestAPIGet_Found(t *testing.T) {
	st := &fakeStore{page: &ListingPage{}}
	srv := newTestServer(t, st)
	defer srv.Close()
	client := loginForm(t, srv)

	resp, err := client.Get(srv.URL + "/admin/listings/42")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"id": 42`) {
		t.Fatalf("get: status=%d body=%s", resp.StatusCode, body)
	}
}

type nilStore struct{}

func (n *nilStore) List(ctx context.Context, f ListingFilters) (*ListingPage, error) {
	return &ListingPage{Items: []ListingRow{}}, nil
}

func (n *nilStore) Get(ctx context.Context, id int64) (*ListingRow, error) {
	return nil, nil
}
