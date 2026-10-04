package admin

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Бэкенд-фильтр: условие «есть расчёт доходности» в SQL, комбинируется
// с остальными фильтрами (AND), счётчик считает по тому же JOIN.
func TestBuildQuery_HasROI(t *testing.T) {
	f := ListingFilters{HasROI: boolp(true), Limit: 10, Page: 1}
	listSQL, countSQL, _ := f.buildQuery()
	if !strings.Contains(listSQL, "ad_roi_results.status = 'ok'") {
		t.Fatalf("list SQL missing has_roi condition:\n%s", listSQL)
	}
	if !strings.Contains(listSQL, "yield_unfurnished_pct IS NOT NULL OR ad_roi_results.yield_furnished_pct IS NOT NULL") {
		t.Fatal("has_roi must require at least one computed scenario")
	}
	if !strings.Contains(countSQL, "LEFT JOIN ad_roi_results") {
		t.Fatalf("count SQL missing ROI join (counter must respect the filter):\n%s", countSQL)
	}

	// Комбинирование с остальными фильтрами: AND.
	f2 := ListingFilters{HasROI: boolp(true), City: "Санкт-Петербург", PriceMin: 7_000_000, Limit: 10, Page: 1}
	l2, _, args := f2.buildQuery()
	if !strings.Contains(l2, "city = $1") || !strings.Contains(l2, "price >= $2") {
		t.Fatalf("combined filters broken:\n%s", l2)
	}
	if !strings.Contains(l2, " AND ad_roi_results.status = 'ok'") {
		t.Fatal("has_roi must be ANDed with other filters")
	}
	if len(args) != 2 {
		t.Fatalf("args = %v, want 2 (has_roi is parameterless)", args)
	}

	// Выключенный/не заданный фильтр ничего не добавляет.
	l3, c3, _ := (&ListingFilters{Limit: 10, Page: 1}).buildQuery()
	if strings.Contains(l3, "ad_roi_results.status") || strings.Contains(c3, "ad_roi_results.status") {
		t.Fatal("has_roi condition leaked into unfiltered query")
	}
	tfalse := false
	l4, _, _ := (&ListingFilters{HasROI: &tfalse, Limit: 10, Page: 1}).buildQuery()
	if strings.Contains(l4, "ad_roi_results.status") {
		t.Fatal("has_roi=false must not filter")
	}
}

// Состояние фильтра сохраняется в ссылках сортировки/пагинации.
func TestQse_PreservesHasROI(t *testing.T) {
	f := ListingFilters{HasROI: boolp(true), SortBy: "price", SortDir: "asc"}
	link := funcs["qse"].(func(ListingFilters, string, string) string)(f, "page", "2")
	if !strings.Contains(link, "has_roi=1") {
		t.Fatalf("pagination link lost has_roi: %s", link)
	}
	f2 := ListingFilters{SortBy: "price", SortDir: "asc"}
	link2 := funcs["qse"].(func(ListingFilters, string, string) string)(f2, "page", "2")
	if strings.Contains(link2, "has_roi") {
		t.Fatalf("has_roi must not appear when filter off: %s", link2)
	}
}

// Чекбокс фильтра: checked при has_roi=1, пустой иначе; параметр
// разбирается обработчиком (e2e через тестовый сервер).
func TestHasROI_E2ECheckbox(t *testing.T) {
	srv := newTestServer(t, &fakeStore{page: &ListingPage{
		Total: 1, Page: 1, Limit: 50,
		Items: []ListingRow{{ID: 1, Title: "Студия", ROI: roiOK()}},
	}})
	defer srv.Close()
	client := loginForm(t, srv)

	// Ссылка с query-параметром открывает список с тем же фильтром.
	resp, err := client.Get(srv.URL + "/admin?has_roi=1&sort=price&dir=asc")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp.Body)
	resp.Body.Close()
	if !strings.Contains(body, `name="has_roi" value="1" checked`) {
		t.Fatal("checkbox not checked from URL param")
	}

	// Без параметра — не отмечен.
	resp2, err := client.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	body2 := readAll(t, resp2.Body)
	resp2.Body.Close()
	if strings.Contains(body2, `name="has_roi" value="1" checked`) {
		t.Fatal("checkbox must be unchecked without has_roi param")
	}
}

// Интеграционный: живой PostgreSQL — фильтр оставляет только объявления
// с посчитанной доходностью, счётчик и комбинирование корректны.
func TestStore_HasROI_Integration(t *testing.T) {
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		t.Skip("TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	store := NewStore(pool)

	const complex = "ЖК РОИ-Фильтр"
	ids := []int64{3221234701, 3221234702, 3221234703}
	for _, id := range ids {
		pool.Exec(ctx, `DELETE FROM ad_roi_results WHERE ad_id=$1`, id)
		pool.Exec(ctx, `DELETE FROM avito_listings WHERE id=$1`, id)
	}
	defer func() {
		for _, id := range ids {
			pool.Exec(ctx, `DELETE FROM ad_roi_results WHERE ad_id=$1`, id)
			pool.Exec(ctx, `DELETE FROM avito_listings WHERE id=$1`, id)
		}
	}()
	for _, id := range ids {
		if _, err := pool.Exec(ctx, `
			INSERT INTO avito_listings (id, url, url_path, category, deal_type, title, price,
			                            studio, total_area, address, residential_complex, raw, params)
			VALUES ($1, $2, $2, 'kvartiry', 'sale', 'Студия 24 м²', 7600000, true, 24, 'СПб, тест', $3, '{}'::jsonb, '{}'::jsonb)`,
			id, "https://www.avito.ru/spb/kvartiry/roifilter_"+strconv.FormatInt(id, 10), complex); err != nil {
			t.Fatalf("seed %d: %v", id, err)
		}
	}
	// 1: посчитан; 2: no_rent_data («—», должен скрыться); 3: расчёта нет.
	if _, err := pool.Exec(ctx, `
		INSERT INTO ad_roi_results (ad_id, status, yield_unfurnished_pct, total_cost_unfurnished, cluster_n, confidence)
		VALUES ($1, 'ok', 4.5, 7929000, 5, 'medium')`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ad_roi_results (ad_id, status, notice)
		VALUES ($1, 'no_rent_data', 'нет арендных данных')`, ids[1]); err != nil {
		t.Fatal(err)
	}

	// Только с рентабельностью: в тестовом ЖК остаётся одна (база живая,
	// поэтому скоупим по ЖК); no_rent_data и без кэша — скрыты.
	page, err := store.List(ctx, ListingFilters{HasROI: boolp(true), Complex: complex, Limit: 10, Page: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != ids[0] {
		t.Fatalf("has_roi: total=%d items=%d %+v, want only %d", page.Total, len(page.Items), page.Items, ids[0])
	}

	// На общем списке сиды 2 (no_rent_data) и 3 (без кэша) отсутствуют.
	pageAll, err := store.List(ctx, ListingFilters{HasROI: boolp(true), Limit: 500, Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, it := range pageAll.Items {
		seen[it.ID] = true
	}
	if seen[ids[1]] || seen[ids[2]] {
		t.Fatalf("hidden listings leaked into has_roi filter: %v/%v present", ids[1], ids[2])
	}
	if !seen[ids[0]] {
		t.Fatalf("computed listing %d missing from has_roi page", ids[0])
	}
	if pageAll.Total < len(pageAll.Items) || pageAll.Total == 0 {
		t.Fatalf("total=%d must be >= page items and non-zero", pageAll.Total)
	}

	// Комбинирование: с чужим ЖК → 0.
	page3, err := store.List(ctx, ListingFilters{HasROI: boolp(true), Complex: "ЖК Другой", Limit: 10, Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page3.Total != 0 || len(page3.Items) != 0 {
		t.Fatalf("has_roi+other complex: total=%d, want 0", page3.Total)
	}
}

func boolp(v bool) *bool { return &v }
