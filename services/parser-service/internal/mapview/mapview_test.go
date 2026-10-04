package mapview

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
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

// Границы диапазонов по метрикам из окружения (не хардкод);
// мусор игнорируется, сортировка сохраняется.
func TestThresholdsFromEnv(t *testing.T) {
	t.Setenv("YIELD_THRESHOLDS", "8, 5 ,x,6")
	t.Setenv("PRICE_THRESHOLDS", "7500000,9000000")
	th := thresholdsFromEnv()
	wantYield := []float64{5, 6, 8}
	if len(th.Yield) != len(wantYield) {
		t.Fatalf("yield = %v, want %v", th.Yield, wantYield)
	}
	for i := range wantYield {
		if th.Yield[i] != wantYield[i] {
			t.Fatalf("yield[%d] = %v, want %v", i, th.Yield[i], wantYield[i])
		}
	}
	if len(th.Price) != 2 || th.Price[0] != 7_500_000 {
		t.Fatalf("price = %v", th.Price)
	}
	// дефолты из макета #107: доходность 5/8, цена 7,5/9 млн, стоимость 9,5/11,5 млн
	t.Setenv("YIELD_THRESHOLDS", "")
	t.Setenv("PRICE_THRESHOLDS", "")
	th2 := thresholdsFromEnv()
	if len(th2.Yield) != 2 || th2.Yield[0] != 5 {
		t.Fatalf("yield defaults = %v", th2.Yield)
	}
	if len(th2.Cost) != 2 || th2.Cost[0] != 9_500_000 {
		t.Fatalf("cost defaults = %v", th2.Cost)
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
	if len(out.Thresholds.Yield) < 2 || len(out.Thresholds.Price) < 2 || len(out.Thresholds.Cost) < 2 {
		t.Fatalf("metric thresholds not in response: %+v", out.Thresholds)
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
		// цвета точек/кластеров/легенды — семантические токены (#108)
		".cluster-pin.b0{background:var(--color-danger)", ".cluster-pin.b1{background:var(--color-warning)",
		".cluster-pin.b2{background:var(--color-success)", "--map-bucket-gray",
		".ldot.b2{background",
		// дизайн-токены #88 + фидбек: без кнопки «Применить»,
		// без отдельной кнопки темы (тему ведёт выбор подложки)
		"--color-accent", "[data-theme=\"dark\"]", "prefers-color-scheme",
		"data-lucide", "Inter",
		// #108: бургер-навигация и профиль; ключевой блок карточки
		"ИнвестКвартал", "burger", "drawer", "user-round",
		"key-data", "Ожидаемая цена сдачи",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("page missing %q", want)
		}
		for _, gone := range []string{"f-apply", "theme-toggle", "flat-sense — данные объявлений"} {
			if strings.Contains(html, gone) {
				t.Fatalf("устаревший элемент %q должен отсутствовать", gone)
			}
		}
	}
}

// Критерий приёмки #88: в клиентском коде карты нет хардкод-цветов —
// все цвета живут в tokens.gen.css (генерируется из auth-flow.pen);
// токены инжектируются в страницу вместе с тёмной темой.
func TestMapJS_NoHardcodedColors(t *testing.T) {
	js, err := staticFS.ReadFile("map.js")
	if err != nil {
		t.Fatal(err)
	}
	hexRe := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)
	// комментарии не сканируем: там встречаются ссылки на issue («#108»)
	noComments := regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*`).ReplaceAll(js, nil)
	if m := hexRe.Find(noComments); m != nil {
		t.Fatalf("map.js contains hardcoded color %q — используйте CSS-переменные", m)
	}
	css, err := staticFS.ReadFile("tokens.gen.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--color-accent:", "[data-theme=\"dark\"]", "--map-bucket-0:", "prefers-color-scheme"} {
		if !bytes.Contains(css, []byte(want)) {
			t.Fatalf("tokens.gen.css missing %q", want)
		}
	}
}
