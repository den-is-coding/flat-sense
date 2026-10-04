package evaluate

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Загружает fixtures (реальные студии из выгрузки парсера + арендные
// аналоги на тех же домах) и возвращает Evaluator на инлайн-источнике.
func fixturesEvaluator(t *testing.T) *Evaluator {
	t.Helper()
	dir := filepath.Join("testdata", "fixtures")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("fixtures недоступны: %v", err)
	}
	src := &mapSource{byID: map[int64]*Listing{}}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		blob, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		l, err := ParseListing(blob)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		src.byID[l.ID] = l
		if l.DealType == "rent_long" {
			src.rents = append(src.rents, l)
		}
	}
	return NewEvaluator(src, DefaultConfig())
}

// mapSource — минимальный Source поверх фикстур (дамп-источник из
// internal/source тестируется отдельно, без импорт-цикла).
type mapSource struct {
	byID  map[int64]*Listing
	rents []*Listing
}

func (s *mapSource) ListingByID(_ context.Context, id int64) (*Listing, error) {
	l, ok := s.byID[id]
	if !ok {
		return nil, errNotFound(id)
	}
	return l, nil
}

func (s *mapSource) RentListings(_ context.Context) ([]Listing, error) {
	out := make([]Listing, 0, len(s.rents))
	for _, l := range s.rents {
		out = append(out, *l)
	}
	return out, nil
}

func approxT(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: got %.6f, want %.6f", name, got, want)
	}
}

func errNotFound(id int64) error { return &notFoundError{id} }

type notFoundError struct{ id int64 }

func (e *notFoundError) Error() string { return "listing not found" }

// E2E (issue #64, критерий 1): студия без мебели в доме с арендным пулом →
// обе строки сценария, арифметика проверена вручную по ценам фикстур.
//
// Дом 175634, аренда: без мебели 26000/27000/28000/32000 (медиана 27500),
// с мебелью 33000/35000/36000 (медиана 35000); 29000 без признака — в
// кластере, но вне групп; 45000 @34 м² — вне диапазона площади.
// Вход: цена 7 958 145 ₽, площадь 21.2 м².
func TestEvalUnfurnishedBothScenarios(t *testing.T) {
	ev := fixturesEvaluator(t)
	rep, err := ev.EvaluateByID(context.Background(), 3651684187)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "ok" {
		t.Fatalf("status = %s (%s)", rep.Status, rep.Notice)
	}
	if rep.InputFurnishing != Unfurnished || rep.FurnishingNote == "" {
		t.Fatalf("меблировка входа: %s (%s)", rep.InputFurnishing, rep.FurnishingNote)
	}
	c := rep.Cluster
	if c.N != 8 {
		t.Fatalf("кластер N = %d, want 8 (8 comps в диапазоне, один уехал по площади)", c.N)
	}
	if c.NUnfurnished != 4 || c.NFurnished != 3 {
		t.Fatalf("разбивка: unfurnished=%d furnished=%d, want 4/3", c.NUnfurnished, c.NFurnished)
	}
	if len(rep.Scenarios) != 2 {
		t.Fatalf("сценариев %d, want 2", len(rep.Scenarios))
	}

	unf, furn := rep.Scenarios[0], rep.Scenarios[1]
	if unf.Name != "без мебели" || !unf.Applicable {
		t.Fatalf("сценарий 1: %+v", unf)
	}
	approxT(t, "rent median (без мебели)", unf.RentMedian, 27500, 1e-9)
	approxT(t, "rent p25", unf.RentP25, 26750, 1e-9)
	approxT(t, "rent p75", unf.RentP75, 29000, 1e-9)
	if unf.Comps != 4 || unf.PriceUsed != 7_958_145 || unf.FurnishingCost != 0 {
		t.Fatalf("аргументы сценария 1: %+v", unf)
	}
	// 7 958 145 / (27 500 × 12) = 24.1156…
	approxT(t, "payback", unf.PaybackYears, 7_958_145.0/330_000.0, 1e-9)
	approxT(t, "yield", unf.YieldPct, 330_000.0/7_958_145.0*100, 1e-9)

	if furn.Name != "с мебелью (после меблировки)" || !furn.Applicable {
		t.Fatalf("сценарий 2: %+v", furn)
	}
	if furn.Comps != 3 {
		t.Fatalf("компов с мебелью = %d, want 3", furn.Comps)
	}
	approxT(t, "rent median (с мебелью)", furn.RentMedian, 35000, 1e-9)
	if furn.PriceUsed != 8_458_145 || furn.FurnishingCost != 500_000 {
		t.Fatalf("надбавка: %+v", furn)
	}
	// 8 458 145 / (35 000 × 12) = 20.1384…
	approxT(t, "payback с надбавкой", furn.PaybackYears, 8_458_145.0/420_000.0, 1e-9)
	approxT(t, "yield с надбавкой", furn.YieldPct, 420_000.0/8_458_145.0*100, 1e-9)

	if rep.Confidence != "high" {
		t.Fatalf("confidence = %s, want high (n=8)", rep.Confidence)
	}
}

// E2E (критерий 2): студия с мебелью → только сценарий «с мебелью», без надбавки.
func TestEvalFurnishedSingleScenario(t *testing.T) {
	ev := fixturesEvaluator(t)
	rep, err := ev.EvaluateByID(context.Background(), 3651723086)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "ok" {
		t.Fatalf("status = %s", rep.Status)
	}
	if rep.InputFurnishing != Furnished {
		t.Fatalf("меблировка входа = %s, want furnished", rep.InputFurnishing)
	}
	if len(rep.Scenarios) != 1 {
		t.Fatalf("сценариев %d, want 1 (только «с мебелью»)", len(rep.Scenarios))
	}
	s := rep.Scenarios[0]
	if s.Name != "с мебелью" || !s.Applicable {
		t.Fatalf("сценарий: %+v", s)
	}
	if s.FurnishingCost != 0 || s.PriceUsed != 7_979_597 {
		t.Fatalf("надбавка не должна применяться: %+v", s)
	}
	approxT(t, "rent median", s.RentMedian, 35000, 1e-9)
	approxT(t, "payback", s.PaybackYears, 7_979_597.0/420_000.0, 1e-9)
}

// E2E (критерий 3): дом без арендных данных → вежливый отказ, не ошибка.
func TestEvalNoRentData(t *testing.T) {
	ev := fixturesEvaluator(t)
	rep, err := ev.EvaluateByID(context.Background(), 4323688397) // дом 184259 без аренды
	if err != nil {
		t.Fatalf("отказ без данных не должен быть ошибкой: %v", err)
	}
	if rep.Status != "no_rent_data" || rep.Notice == "" {
		t.Fatalf("status = %q, notice = %q", rep.Status, rep.Notice)
	}
	if rep.Listing == nil || rep.Listing.ID != 4323688397 {
		t.Fatal("данные объявления должны присутствовать в ответе")
	}
}

// E2E (критерий 5): кластер n < 3 → пониженный confidence и предупреждение.
func TestEvalSmallClusterWarning(t *testing.T) {
	ev := fixturesEvaluator(t)
	rep, err := ev.EvaluateByID(context.Background(), 7953363732) // дом 2041684: 2 comps
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "ok" {
		t.Fatalf("status = %s", rep.Status)
	}
	if rep.Cluster.N != 2 {
		t.Fatalf("N = %d, want 2", rep.Cluster.N)
	}
	if rep.Confidence != "low" {
		t.Fatalf("confidence = %s, want low", rep.Confidence)
	}
	if len(rep.Warnings) == 0 {
		t.Fatal("должно быть предупреждение о малом кластере")
	}
}

// Диапазон площади — границы из конфига (проверка на фикстурах: 34 м²
// не входит в ±20% от 21.2 м²).
func TestEvalAreaRangeFromConfig(t *testing.T) {
	ev := fixturesEvaluator(t)
	ev.Config.AreaTolerancePct = 100 // расширяем: 34 м² теперь входит
	rep, err := ev.EvaluateByID(context.Background(), 3651684187)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Cluster.N != 9 {
		t.Fatalf("N = %d, want 9 (добавился комп 34 м²)", rep.Cluster.N)
	}
}
