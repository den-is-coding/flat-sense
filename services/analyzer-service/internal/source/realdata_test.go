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
// fixtures) против реальной аренды того же дома — 10 студий в дампе
// кампании. Совпадение дома — по нормализованному адресу: арендные items
// houseLink не содержат.
//
// Данные дома 71к2 (после омоглиф-нормализации описаний): мебель указали
// 2 из 10 (35 000 и 36 000 ₽ → медиана 35 500), «без мебели» явно — 0,
// не указана — 8 → для сценария «без мебели» работает фолбэк на медиану
// всего кластера: цены [30000,30000,35000,35000,36000,39000,39600,40000,
// 43000,45000] → медиана 37 500, p25 35 000, p75 39 900.
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
	if c.N != 10 {
		t.Fatalf("N = %d, want 10 (аренда дома 71к2)", c.N)
	}
	if c.NFurnished != 2 || c.NUnfurnished != 0 {
		t.Fatalf("разбивка: furnished=%d unfurnished=%d, want 2/0", c.NFurnished, c.NUnfurnished)
	}
	if rep.InputFurnishing != evaluate.Unfurnished {
		t.Fatalf("меблировка входа = %s (описание пустое → консервативно «без мебели»)", rep.InputFurnishing)
	}
	if len(rep.Scenarios) != 2 {
		t.Fatalf("сценариев %d, want 2", len(rep.Scenarios))
	}

	unf, furn := rep.Scenarios[0], rep.Scenarios[1]
	// «без мебели»: фолбэк на весь кластер — медиана 37 500, вилка 35 000–39 900.
	if unf.Comps != 10 {
		t.Fatalf("компов «без мебели» = %d, want 10 (фолбэк)", unf.Comps)
	}
	approxT(t, "rent median (фолбэк)", unf.RentMedian, 37500, 1e-9)
	approxT(t, "rent p25", unf.RentP25, 35000, 1e-9)
	approxT(t, "rent p75", unf.RentP75, 39900, 1e-9)
	if unf.PriceUsed != 7_990_000 || unf.FurnishingCost != 0 {
		t.Fatalf("аргументы «без мебели»: %+v", unf)
	}
	// 7 990 000 / (37 500 × 12) = 17.7555… лет
	approxT(t, "payback", unf.PaybackYears, 7_990_000.0/450_000.0, 1e-9)
	approxT(t, "yield", unf.YieldPct, 450_000.0/7_990_000.0*100, 1e-9)

	// «с мебелью»: строгая группа — медиана 35 500, цена +500 000.
	if furn.Comps != 2 {
		t.Fatalf("компов «с мебелью» = %d, want 2", furn.Comps)
	}
	approxT(t, "rent median (с мебелью)", furn.RentMedian, 35500, 1e-9)
	if furn.PriceUsed != 8_490_000 {
		t.Fatalf("PriceUsed = %d, want 8 490 000 (7 990 000 + 500 000)", furn.PriceUsed)
	}
	// 8 490 000 / (35 500 × 12) = 19.9295… лет
	approxT(t, "payback с надбавкой", furn.PaybackYears, 8_490_000.0/426_000.0, 1e-9)
	approxT(t, "yield с надбавкой", furn.YieldPct, 426_000.0/8_490_000.0*100, 1e-9)

	if rep.Confidence != "high" {
		t.Fatalf("confidence = %s, want high (n=10)", rep.Confidence)
	}
	if len(rep.Warnings) == 0 {
		t.Fatal("должно быть предупреждение о фолбэке (8 из 10 без указания мебели)")
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
