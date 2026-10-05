package admin

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// roiOK — строка с посчитанной окупаемостью (оба сценария).
func roiOK() *ROIView {
	return &ROIView{
		Status:                "ok",
		InputFurnishing:       "unfurnished",
		YieldUnfurnished:      f64p(4.54),
		TotalCostUnfurnished:  i64p(7_929_000),
		RentMedianUnfurnished: f64p(30_000),
		CompsUnfurnished:      i32p(1),
		YieldFurnished:        f64p(4.26),
		TotalCostFurnished:    i64p(8_429_000),
		RentMedianFurnished:   f64p(34_000),
		CompsFurnished:        i32p(1),
		ClusterN:              i32p(2),
		Confidence:            "low",
	}
}

// Таблица: посчитанное объявление показывает две доходности (один знак),
// полную стоимость и подпись методики в тултипе.
func TestRender_ROIValues(t *testing.T) {
	body := renderTableBody(t, ListingRow{ID: 1, Title: "Студия", ROI: roiOK()})
	for _, want := range []string{
		"4.5",       // доходность без мебели, 1 знак
		"4.3",       // с мебелью
		"8 429 000", // полная стоимость (сценарий «с мебелью»)
		"медиана сдачи 34 000 ₽/мес", // тултип
		"уверенность low",
		"«без мебели»: 7 929 000 ₽", // вторая стоимость в тултипе
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered table missing %q\n%s", want, body)
		}
	}
}

// Таблица: нет арендных данных → «—» во всех трёх колонках, причина —
// в тултипе; это не ноль и не пустая строка.
func TestRender_ROINoRentData(t *testing.T) {
	body := renderTableBody(t, ListingRow{ID: 2, Title: "Студия", ROI: &ROIView{
		Status: "no_rent_data",
		Notice: "по дому/ЖК addr:… нет арендных данных",
	}})
	if got := strings.Count(body, ">—<"); got < 3 {
		t.Fatalf("want ≥3 dash cells, got %d\n%s", got, body)
	}
	if !strings.Contains(body, "нет арендных данных") {
		t.Fatal("notice (reason) not in tooltip")
	}
	// ноль как значение недопустим
	if strings.Contains(body, "0.0") {
		t.Fatalf("zero rendered instead of dash:\n%s", body)
	}
}

// Таблица: расчёта ещё не было (строки кэша нет) → «—» без тултипа-причины.
func TestRender_ROIMissing(t *testing.T) {
	body := renderTableBody(t, ListingRow{ID: 3, Title: "Студия"})
	if got := strings.Count(body, ">—<"); got < 3 {
		t.Fatalf("want ≥3 dash cells, got %d\n%s", got, body)
	}
	if !strings.Contains(body, "backfill-roi") {
		t.Fatal("hint about backfill not shown for missing calculation")
	}
}

// Сценарий неприменим (нет аналогов с мебелью), а расчёт есть: доходность
// «—», но полная стоимость «без мебели» показывается.
func TestRender_ROIPartialScenario(t *testing.T) {
	body := renderTableBody(t, ListingRow{ID: 4, Title: "Студия", ROI: &ROIView{
		Status:               "ok",
		TotalCostUnfurnished: i64p(7_929_000),
		Notice:               "с мебелью (после меблировки): нет данных",
	}})
	if got := strings.Count(body, ">—<"); got < 2 {
		t.Fatalf("want ≥2 dash cells (yields), got %d\n%s", got, body)
	}
	if !strings.Contains(body, "7 929 000") {
		t.Fatal("unfurnished cost not rendered")
	}
}

// Селект списка обязан джойнить кэш ad_roi_results.
func TestBuildQuery_JoinsROI(t *testing.T) {
	listSQL, _, _ := (&ListingFilters{Limit: 10, Page: 1}).buildQuery()
	if !strings.Contains(listSQL, "LEFT JOIN ad_roi_results") {
		t.Fatalf("list SQL missing ROI join:\n%s", listSQL)
	}
	if !strings.Contains(listSQL, "yield_unfurnished_pct") {
		t.Fatal("yield columns not selected")
	}
}

// pct1 — один знак после запятой; nil → пусто.
func TestPct1(t *testing.T) {
	if s := pct1(f64p(4.5404)); s != "4.5" {
		t.Fatalf("pct1 = %q, want 4.5", s)
	}
	if s := pct1(f64p(5.06)); s != "5.1" {
		t.Fatalf("pct1 = %q, want 5.1", s)
	}
	if s := pct1(nil); s != "" {
		t.Fatalf("pct1(nil) = %q, want empty", s)
	}
}

// renderTableBody — HTML таблицы для одной строки.
func renderTableBody(t *testing.T, row ListingRow) string {
	t.Helper()
	srv := newTestServer(t, &fakeStore{page: &ListingPage{
		Total: 1, Page: 1, Limit: 50, Items: []ListingRow{row},
	}})
	defer srv.Close()
	client := loginForm(t, srv)
	resp, err := client.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	return readAll(t, resp.Body)
}

// Интеграционный тест: живой PostgreSQL с миграциями 000010–000014.
// Строка кэша ad_roi_results доезжает до List/Get (JOIN), объявление
// без кэша отдаёт roi=nil.
func TestStore_ROI_Integration(t *testing.T) {
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

	const withROI, noROI = int64(3221234601), int64(3221234602)
	for _, id := range []int64{withROI, noROI} {
		pool.Exec(ctx, `DELETE FROM ad_roi_results WHERE ad_id=$1`, id)
		pool.Exec(ctx, `DELETE FROM avito_listings WHERE id=$1`, id)
	}
	// Очистка до Close (t.Cleanup выполняется после defer pool.Close).
	defer func() {
		pool.Exec(ctx, `DELETE FROM ad_roi_results WHERE ad_id=$1`, withROI)
		pool.Exec(ctx, `DELETE FROM avito_listings WHERE id=$1`, withROI)
		pool.Exec(ctx, `DELETE FROM avito_listings WHERE id=$1`, noROI)
	}()
	for _, s := range []struct {
		id      int64
		complex string
	}{{withROI, "ЖК Граффити (тест)"}, {noROI, "ЖК Без-расчёта"}} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO avito_listings (id, url, url_path, category, deal_type, title, price,
			                            studio, total_area, address, residential_complex, raw, params)
			VALUES ($1, $2, $2, 'kvartiry', 'sale', 'Студия 24 м²', 7600000, true, 24, 'СПб, тест', $3, '{}'::jsonb, '{}'::jsonb)`,
			s.id, "https://www.avito.ru/spb/kvartiry/roi_test_"+strconv.FormatInt(s.id, 10), s.complex); err != nil {
			t.Fatalf("seed %d: %v", s.id, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ad_roi_results (ad_id, status, input_furnishing,
			yield_unfurnished_pct, total_cost_unfurnished, rent_median_unfurnished, comps_unfurnished,
			cluster_n, confidence, notice)
		VALUES ($1, 'ok', 'unfurnished', 4.54, 7929000, 30000, 2, 2, 'low', '')`, withROI); err != nil {
		t.Fatalf("seed roi: %v", err)
	}

	// List: обе строки приходят, у первой ROI заполнен, у второй nil.
	page, err := store.List(ctx, ListingFilters{Complex: "ЖК Граффити (тест)", Limit: 10, Page: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("list: total=%d items=%d, want 1/1", page.Total, len(page.Items))
	}
	got := page.Items[0]
	if got.ROI == nil || got.ROI.YieldUnfurnished == nil || *got.ROI.YieldUnfurnished != 4.54 {
		t.Fatalf("roi via List = %+v", got.ROI)
	}
	if got.ROI.TotalCostFurnished != nil {
		t.Fatal("totalCostFurnished must be NULL («—»), not a number")
	}

	// Get: то же по одному объявлению.
	row, err := store.Get(ctx, withROI)
	if err != nil || row == nil || row.ROI == nil || row.ROI.Confidence != "low" {
		t.Fatalf("get with roi: %v %+v", err, row)
	}
	row2, err := store.Get(ctx, noROI)
	if err != nil || row2 == nil {
		t.Fatalf("get without roi: %v %v", err, row2)
	}
	if row2.ROI != nil {
		t.Fatalf("roi must be nil when no cache row, got %+v", row2.ROI)
	}
}

func i32p(v int) *int { return &v }
