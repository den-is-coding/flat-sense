package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// Пути к реальным арендным дампам кампаний (закоммичены в репозитории:
// parser-service testdata). Формат {meta, items}.
const realRentDump = "../../../parser-service/internal/avito/testdata/" +
	"avito_run_arenda_primprime_2026-10-04/avito_arenda_studii_prame_primorskiy_2026-10-04.json"

const graffitiDump = "../../../parser-service/internal/avito/testdata/" +
	"avito_run_arenda_graffiti_2026-10-04/avito_arenda_studii_graffiti_2026-10-04.json"

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
// fixtures) против реальной аренды ЖК «Прайм Приморский» — кластер уровня
// ЖК: кампания покрывает 4 корпуса (Парашютная 71к2/77к1/79к1, Лидии
// Зверевой 6), каждый item получает алиасы по всем адресам кампании.
//
// Данные (после омоглиф-нормализации и предметного признака мебели),
// весь кластер без фильтра площади — 24 студии: «без мебели» явно — 3
// (25 000, 25 000, 26 000 → медиана 25 000); «с мебелью» — 21 (явные фразы
// и перечисления предметов: медиана 36 000, p25 32 000, p75 40 000);
// не определена — 0.
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
	if c.N != 24 {
		t.Fatalf("N = %d, want 24 (весь ЖК)", c.N)
	}
	if c.NFurnished != 21 || c.NUnfurnished != 3 {
		t.Fatalf("разбивка: furnished=%d unfurnished=%d, want 21/3", c.NFurnished, c.NUnfurnished)
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
	if unf.PriceUsed != 8_334_600 || unf.FurnishingCost != 0 || unf.DealCosts != 344_600 {
		t.Fatalf("аргументы «без мебели»: %+v", unf)
	}
	// цена 7 990 000 + сделка 344 600 (4% = 319 600 + 25 000) = 8 334 600;
	// годовая аренда 300 000 ₽: окупаемость 27.782 лет
	approxT(t, "payback", unf.PaybackYears, 8_334_600.0/300_000.0, 1e-9)
	approxT(t, "yield", unf.YieldPct, 300_000.0/8_334_600.0*100, 1e-9)

	// «с мебелью»: строгая группа 21 аналог → медиана 36 000, p25 32 000, p75 40 000.
	if furn.Comps != 21 {
		t.Fatalf("компов «с мебелью» = %d, want 21", furn.Comps)
	}
	approxT(t, "rent median (с мебелью)", furn.RentMedian, 36000, 1e-9)
	approxT(t, "rent p25", furn.RentP25, 32000, 1e-9)
	approxT(t, "rent p75", furn.RentP75, 40000, 1e-9)
	if furn.PriceUsed != 8_834_600 || furn.FurnishingCost != 500_000 || furn.DealCosts != 344_600 {
		t.Fatalf("аргументы «с мебелью»: %+v", furn)
	}
	// 7 990 000 + 500 000 + 344 600 = 8 834 600; годовая аренда 432 000 ₽
	approxT(t, "payback с надбавкой", furn.PaybackYears, 8_834_600.0/432_000.0, 1e-9)
	approxT(t, "yield с надбавкой", furn.YieldPct, 432_000.0/8_834_600.0*100, 1e-9)

	if rep.Confidence != "high" {
		t.Fatalf("confidence = %s, want high (n=24)", rep.Confidence)
	}
	if len(rep.Warnings) != 0 {
		t.Fatalf("предупреждений быть не должно (обе строгие группы непусты): %v", rep.Warnings)
	}
}

// Площадь/этаж извлекаются из заголовка арендных items кампании:
// фактический разброс площадей кластера ЖК — от 24.0 до 29.9 м².
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
	if rep.Cluster.AreaRange[0] > 24.0+1e-9 || rep.Cluster.AreaRange[1] < 29.9-1e-9 {
		t.Fatalf("диапазон площади: %v, want [24.0, 29.9]", rep.Cluster.AreaRange)
	}
}

// Граффити (issue #64): пул 2 объявления, оба меблированы (по предметному
// признаку: кровать/шкаф/диван/телевизор в первом, кровать/стол/кухня во
// втором). «С мебелью» считается; «без мебели» — «нет данных».
func TestEvalGraffitiFurnishedDetected(t *testing.T) {
	if _, err := os.Stat(graffitiDump); err != nil {
		t.Skipf("дамп Граффити недоступен: %v", err)
	}
	src, err := NewDumpSource(
		filepath.Join("..", "evaluate", "testdata", "fixtures"),
		filepath.Dir(graffitiDump),
	)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := evaluate.NewEvaluator(src, evaluate.DefaultConfig()).
		EvaluateByID(context.Background(), 8354563977) // Парашютная 42к1, 24.0 м², 7 600 000 ₽
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "ok" {
		t.Fatalf("status = %s (%s)", rep.Status, rep.Notice)
	}
	if rep.Cluster.N != 2 {
		t.Fatalf("N = %d, want 2 (весь пул Граффити)", rep.Cluster.N)
	}
	if rep.Cluster.NFurnished != 2 || rep.Cluster.NUnfurnished != 0 {
		t.Fatalf("разбивка: %d/%d, want 2/0 (оба меблированы)", rep.Cluster.NFurnished, rep.Cluster.NUnfurnished)
	}
	if len(rep.Scenarios) != 2 {
		t.Fatalf("сценариев %d, want 2", len(rep.Scenarios))
	}
	unf, furn := rep.Scenarios[0], rep.Scenarios[1]
	if unf.Applicable {
		t.Fatalf("«без мебели» не должен считаться без явных аналогов: %+v", unf)
	}
	if unf.SkippedReason == "" {
		t.Fatal("«без мебели» должен иметь причину «нет данных»")
	}
	if !furn.Applicable || furn.Comps != 2 {
		t.Fatalf("«с мебелью»: %+v", furn)
	}
	// медиана (28000+35897)/2 = 31948.5
	approxT(t, "rent median (с мебелью)", furn.RentMedian, 31948.5, 1e-9)
	if furn.PriceUsed != 8_429_000 || furn.FurnishingCost != 500_000 || furn.DealCosts != 329_000 {
		t.Fatalf("аргументы «с мебелью»: %+v", furn)
	}
	// годовая аренда 383 382 ₽: окупаемость 21.986 лет
	approxT(t, "payback", furn.PaybackYears, 8_429_000.0/383_382.0, 1e-9)
	if rep.Confidence != "low" {
		t.Fatalf("confidence = %s, want low (n=2)", rep.Confidence)
	}
}
