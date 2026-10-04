package mapview

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// bbox + фильтры собираются в SQL: без координат не выдаётся никогда,
// фильтры AND-ятся, параметры — только позиционные.
func TestBuildQuery(t *testing.T) {
	f := MapFilters{BBox: [4]float64{30.1, 59.9, 30.4, 60.1}, HasROI: true, YieldMin: 5, PriceMax: 9_000_000, Rooms: "studio"}
	sql, args := f.buildQuery()
	for _, frag := range []string{
		"a.lat IS NOT NULL AND a.lng IS NOT NULL",
		"a.lat BETWEEN $1 AND $2", "a.lng BETWEEN $3 AND $4",
		"r.status = 'ok'", "yield_furnished_pct IS NOT NULL",
		"coalesce(r.yield_furnished_pct, r.yield_unfurnished_pct) >= $5",
		"a.price <= $6", "a.studio = true", "LIMIT 5000",
	} {
		if !strings.Contains(sql, frag) {
			t.Fatalf("SQL missing %q:\n%s", frag, sql)
		}
	}
	if len(args) != 6 {
		t.Fatalf("args = %v, want 6", args)
	}

	// rooms: 3plus и конкретное число
	f3p := MapFilters{BBox: f.BBox, Rooms: "3plus"}
	sql2, _ := f3p.buildQuery()
	if !strings.Contains(sql2, "a.rooms >= 3") {
		t.Fatalf("3plus missing:\n%s", sql2)
	}
	f2r := MapFilters{BBox: f.BBox, Rooms: "2"}
	sql3, args3 := f2r.buildQuery()
	if !strings.Contains(sql3, "a.rooms = $5") || len(args3) != 5 {
		t.Fatalf("rooms=2 broken:\n%s args=%v", sql3, args3)
	}

	// без фильтров — только bbox
	fb := MapFilters{BBox: f.BBox}
	sql4, args4 := fb.buildQuery()
	if strings.Contains(sql4, "r.status") || strings.Contains(sql4, "a.price <=") {
		t.Fatalf("filters leaked into bbox-only query:\n%s", sql4)
	}
	if len(args4) != 4 {
		t.Fatalf("bbox args = %v, want 4", args4)
	}
}

// Параметры запроса: bbox парсится и нормализуется, мусор → 400.
func TestFiltersFromRequest(t *testing.T) {
	mk := func(q string) *http.Request {
		return httptest.NewRequest("GET", "/api/map/listings?"+q, nil)
	}
	f, err := filtersFromRequest(mk("bbox=30.1,59.9,30.4,60.1"))
	if err != nil {
		t.Fatal(err)
	}
	if f.BBox != [4]float64{30.1, 59.9, 30.4, 60.1} {
		t.Fatalf("bbox = %v", f.BBox)
	}
	// перепутанный порядок нормализуется
	f2, err := filtersFromRequest(mk("bbox=30.4,60.1,30.1,59.9"))
	if err != nil || f2.BBox[0] != 30.1 || f2.BBox[1] != 59.9 || f2.BBox[2] != 30.4 || f2.BBox[3] != 60.1 {
		t.Fatalf("bbox normalize: %v %v", f2.BBox, err)
	}
	// фильтры
	f3, err := filtersFromRequest(mk("bbox=30.1,59.9,30.4,60.1&has_roi=1&yield_min=5.5&price_max=9000000&rooms=studio"))
	if err != nil {
		t.Fatal(err)
	}
	if !f3.HasROI || f3.YieldMin != 5.5 || f3.PriceMax != 9_000_000 || f3.Rooms != "studio" {
		t.Fatalf("filters = %+v", f3)
	}
	// мусор
	for _, bad := range []string{
		"bbox=30.1,59.9,30.4", "bbox=a,b,c,d", "bbox=10,10,10,10", // вне СПб
		"bbox=30.1,59.9,30.4,60.1&yield_min=-1", "bbox=30.1,59.9,30.4,60.1&price_max=x",
	} {
		if _, err := filtersFromRequest(mk(bad)); err == nil {
			t.Fatalf("expected 400 for %q", bad)
		}
	}
}

// Границы 7 диапазонов из окружения (не хардкод); мусор игнорируется.
func TestThresholdsFromEnv(t *testing.T) {
	t.Setenv("YIELD_THRESHOLDS", "3.8, 4.4 , 5.0,6,x,2")
	th := thresholdsFromEnv()
	want := []float64{2, 3.8, 4.4, 5, 6}
	if len(th.Boundaries) != len(want) {
		t.Fatalf("boundaries = %v, want %v", th.Boundaries, want)
	}
	for i := range want {
		if th.Boundaries[i] != want[i] {
			t.Fatalf("boundaries[%d] = %v, want %v", i, th.Boundaries[i], want[i])
		}
	}
	// дефолт: 6 границ → 7 диапазонов
	t.Setenv("YIELD_THRESHOLDS", "")
	th2 := thresholdsFromEnv()
	if len(th2.Boundaries) != 6 || th2.Boundaries[0] != 4.1 {
		t.Fatalf("defaults = %v", th2.Boundaries)
	}
}

// Живой PostgreSQL: bbox-выдача по реальным данным + фильтр has_roi.
func TestStore_Integration(t *testing.T) {
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		t.Skip("TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := NewStore(pool)

	// весь СПб: точки есть, все с координатами
	items, err := store.List(ctx, MapFilters{BBox: [4]float64{29.5, 59.5, 31.5, 60.5}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("no map items in SPb bbox")
	}
	for _, it := range items {
		if it.Lat == 0 || it.Lng == 0 {
			t.Fatalf("item %d without coords leaked", it.ID)
		}
	}
	// has_roi: только с расчётом, у каждой — хотя бы одна доходность
	roi, err := store.List(ctx, MapFilters{BBox: [4]float64{29.5, 59.5, 31.5, 60.5}, HasROI: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(roi) == 0 || len(roi) > len(items) {
		t.Fatalf("has_roi items = %d (all=%d)", len(roi), len(items))
	}
	for _, it := range roi {
		if it.YieldFurnished == nil && it.YieldUnfurnished == nil {
			t.Fatalf("has_roi item %d has no yield", it.ID)
		}
	}
}

// Маршрутизация API без БД: мусорный bbox → 400.
func TestHandlers_BadRequest(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, NewStore(nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/map/listings?bbox=bad")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad bbox: status = %d, want 400", resp.StatusCode)
	}
}

// JSON-контракт выдачи и SSR-страница на живой БД.
func TestHandlers_API_Integration(t *testing.T) {
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		t.Skip("TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	mux := http.NewServeMux()
	Register(mux, NewStore(pool))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/map/listings?bbox=30.1,59.85,30.5,60.05&has_roi=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out mapResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Thresholds.Boundaries) < 2 {
		t.Fatalf("boundaries not in response: %v", out.Thresholds.Boundaries)
	}
	if len(out.Items) == 0 {
		t.Fatal("no roi items in SPb center bbox")
	}
	it := out.Items[0]
	if it.URL == "" || it.Lat == 0 || it.Lng == 0 {
		t.Fatalf("broken item: %+v", it)
	}
	if it.YieldFurnished == nil && it.YieldUnfurnished == nil {
		t.Fatalf("roi item without yield: %+v", it)
	}

	// SSR-страница: индексируемая (без noindex), тексты и пороги на месте.
	resp2, err := http.Get(srv.URL + "/map")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body, err := io.ReadAll(resp2.Body)
	if err != nil {
		t.Fatal(err)
	}
	if tag := resp2.Header.Get("X-Robots-Tag"); strings.Contains(tag, "noindex") {
		t.Fatal("/map must be indexable")
	}
	html := string(body)
	for _, want := range []string{
		"Карта студий Санкт-Петербурга",
		"OpenStreetMap", // атрибуция обязательна
		"MAP_THRESHOLDS",
		"только с рентабельностью",
		// фон кластера — сплошной, по 7 диапазонам доходности (#73)
		".cluster-pin.c0{background", ".cluster-pin.c3{background",
		".cluster-pin.c6{background", ".cluster-pin.gray{background",
		".dot.c2{background",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("page missing %q", want)
		}
	}
}
