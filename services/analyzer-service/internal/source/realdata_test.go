package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// Путь к реальному арендному дампу кампании (закоммичен в репозитории:
// parser-service testdata). Формат {meta, items}, 24 студии ЖК
// «Прайм Приморский» (Парашютная ул., 71к2/77к1/79к1, Лидии Зверевой 6).
const realRentDump = "../../../parser-service/internal/avito/testdata/" +
	"avito_run_arenda_primprime_2026-10-04/avito_arenda_studii_prame_primorskiy_2026-10-04.json"

// approxT проверяет числа с абсолютной точностью.
func approxT(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	if diff > tol {
		t.Errorf("%s: got %.6f, want %.6f", name, got, want)
	}
}

// E2E на реальных данных (issue #64, критерий 1): sale-объявление
// 8409401832 (Парашютная ул., 71к2, 24.7 м², 7 990 000 ₽ — выгрузка из БД,
// fixtures) против реальной аренды ЖҚ «Прайм Приморский» — кластер уровня
// ЖК: кампания покрывает 4 корпуса (Парашютная 71к2/77к1/79к1, Лидии
// Зверевой 6), каждый item получает алиасы по всем адресам кампании.
//
// Данные (после омоглиф-нормализации): «без мебели» явно — 3 объявления
// Зверевой 6 (25 000, 25 000, 26 000 → медиана 25 000); «с мебелью» — 3
// в диапазоне площади (35 000, 36 000, 40 000 → медиана 36 000; 39 999
// @29.9 м² выпадает по допуску ±20% от 24.7 м²); не указана — 17.
// Итого кластер 23 из 24 (area filter), фолбэк не нужен.
func TestEvalRealRentPrimPrime(t *testing.T) {
	if _, err := os.Stat(realRentDump); err != nil {
		t.Skipf("реальный арендный дамп недоступен: %v", err)
	}
	src, err := NewDumpSource(
		filepath.Join("..", "evaluate", "testdata", "fixtures"),
		filepath.Dir(realRentDump),
	)
	if err != nil {
		t.Fatal(err)
	}
	ev := evaluate.NewEvaluator(src, evaluate.DefaultConfig())
	rep, err := ev.EvaluateByID(context.Background(), 8409401832)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "ok" {
		t.Fatalf("status = %s (%s)", rep.Status, rep.Notice)
	}
	c := rep.Cluster
	if c.N != 23 {
		t.Fatalf("N = %d, want 23 (весь ЖК минус комп 29.9 м²)", c.N)
	}
	if c.NFurnished != 3 || c.NUnfurnished != 3 {
		t.Fatalf("разбивка: furnished=%d unfurnished=%d, want 3/3", c.NFurnished, c.NUnfurnished)
	}
	if rep.InputFurnishing != evaluate.Unfurnished {
		t.Fatalf("меблировка входа = %s (описание пустое → консервативно «без мебели»)", rep.InputFurnishing)
	}
	if len(rep.Scenarios) != 2 {
		t.Fatalf("сценариев %d, want 2", len(rep.Scenarios))
	}

	unf, furn := rep.Scenarios[0], rep.Scenarios[1]
	// «без мебели»: строгая группа — медиана 25 000 (25 000/25 000/26 000).
	if unf.Comps != 3 {
		t.Fatalf("компов «без мебели» = %d, want 3", unf.Comps)
	}
	approxT(t, "rent median (без мебели)", unf.RentMedian, 25000, 1e-9)
	approxT(t, "rent p25", unf.RentP25, 25000, 1e-9)
	approxT(t, "rent p75", unf.RentP75, 25500, 1e-9)
	if unf.PriceUsed != 7_990_000 || unf.FurnishingCost != 0 {
		t.Fatalf("аргументы «без мебели»: %+v", unf)
	}
	// 7 990 000 / (25 000 × 12) = 26.6333… лет
	approxT(t, "payback", unf.PaybackYears, 7_990_000.0/300_000.0, 1e-9)
	approxT(t, "yield", unf.YieldPct, 300_000.0/7_990_000.0*100, 1e-9)

	// «с мебелью»: строгая группа — медиана 36 000 (35 000/36 000/40 000).
	if furn.Comps != 3 {
		t.Fatalf("компов «с мебелью» = %d, want 3", furn.Comps)
	}
	approxT(t, "rent median (с мебелью)", furn.RentMedian, 36000, 1e-9)
	approxT(t, "rent p25", furn.RentP25, 35500, 1e-9)
	approxT(t, "rent p75", furn.RentP75, 38000, 1e-9)
	if furn.PriceUsed != 8_490_000 {
		t.Fatalf("PriceUsed = %d, want 8 490 000 (7 990 000 + 500 000)", furn.PriceUsed)
	}
	// 8 490 000 / (36 000 × 12) = 19.6527… лет
	approxT(t, "payback с надбавкой", furn.PaybackYears, 8_490_000.0/432_000.0, 1e-9)
	approxT(t, "yield с надбавкой", furn.YieldPct, 432_000.0/8_490_000.0*100, 1e-9)

	if rep.Confidence != "high" {
		t.Fatalf("confidence = %s, want high (n=23)", rep.Confidence)
	}
	if len(rep.Warnings) != 0 {
		t.Fatalf("предупреждений быть не должно (обе строгие группы непусты): %v", rep.Warnings)
	}
}

// Площадь/этаж извлекаются из заголовка арендных items кампании:
// вход 24.7 м² ±20% → 19.76–29.64 — все 10 аналогов дома (24.0–27.6) внутри.
func TestCampaignItemsTitleMetrics(t *testing.T) {
	if _, err := os.Stat(realRentDump); err != nil {
		t.Skipf("реальный арендный дамп недоступен: %v", err)
	}
	src, err := NewDumpSource(
		filepath.Join("..", "evaluate", "testdata", "fixtures"),
		filepath.Dir(realRentDump),
	)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := evaluate.NewEvaluator(src, evaluate.DefaultConfig()).
		EvaluateByID(context.Background(), 8409401832)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Cluster == nil {
		t.Fatalf("кластер не построен: status=%s notice=%s", rep.Status, rep.Notice)
	}
	if rep.Cluster.AreaRange[0] > 24.0 || rep.Cluster.AreaRange[1] < 27.6 {
		t.Fatalf("диапазон площади: %v", rep.Cluster.AreaRange)
	}
}
