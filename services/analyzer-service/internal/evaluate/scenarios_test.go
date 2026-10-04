package evaluate

import (
	"math"
	"testing"
)

func approx(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: got %.6f, want %.6f (±%.4f)", name, got, want, tol)
	}
}

// Арифметика сценариев (issue #64): аренда = медиана группы;
// окупаемость = итоговая стоимость / (аренда × 12); доходность = аренда × 12 / стоимость.
func TestComputeScenario(t *testing.T) {
	t.Run("без мебели: без надбавок", func(t *testing.T) {
		// аренда 27500 ₽/мес; цена 7 958 145 ₽, издержки 0
		s := ComputeScenario("без мебели", 27500, 26750, 29000, 4, 7_958_145, 0, 0)
		if !s.Applicable {
			t.Fatal("сценарий должен быть применим")
		}
		if s.PriceUsed != 7_958_145 {
			t.Errorf("PriceUsed = %d", s.PriceUsed)
		}
		if s.FurnishingCost != 0 || s.DealCosts != 0 {
			t.Errorf("FurnishingCost = %d, DealCosts = %d, want 0/0", s.FurnishingCost, s.DealCosts)
		}
		// годовая аренда 330 000 ₽: окупаемость 7 958 145 / 330 000 = 24.1156… лет
		approx(t, "payback", s.PaybackYears, 7_958_145.0/330_000.0, 1e-9)
		approx(t, "yield", s.YieldPct, 330_000.0/7_958_145.0*100, 1e-9)
	})

	t.Run("с мебелью: +500 000 ₽ и издержки сделки", func(t *testing.T) {
		// аренда 35000 ₽/мес; цена 7 958 145 + мебель 500 000 + сделка 343 326 = 8 801 471 ₽
		s := ComputeScenario("с мебелью", 35000, 33000, 36000, 3, 7_958_145, 500_000, 343_326)
		if s.PriceUsed != 8_801_471 {
			t.Errorf("PriceUsed = %d, want 8801471", s.PriceUsed)
		}
		if s.FurnishingCost != 500_000 || s.DealCosts != 343_326 {
			t.Errorf("FurnishingCost = %d, DealCosts = %d", s.FurnishingCost, s.DealCosts)
		}
		// годовая аренда 420 000 ₽: окупаемость 8 801 471 / 420 000 = 20.9559… лет
		approx(t, "payback", s.PaybackYears, 8_801_471.0/420_000.0, 1e-9)
		approx(t, "yield", s.YieldPct, 420_000.0/8_801_471.0*100, 1e-9)
	})

	t.Run("нет аналогов — неприменим с причиной", func(t *testing.T) {
		s := ComputeScenario("с мебелью", 0, 0, 0, 0, 7_958_145, 500_000, 343_326)
		if s.Applicable {
			t.Fatal("сценарий не должен быть применим")
		}
		if s.SkippedReason == "" {
			t.Fatal("должна быть причина пропуска")
		}
	})
}

// Издержки сделки: 3% риэлтор + 1% титул (от цены, с округлением) + фикс.
func TestDealCosts(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.DealCosts(7_990_000); got != 344_600 { // 4% = 319 600 + 25 000
		t.Errorf("DealCosts(7990000) = %d, want 344600", got)
	}
	if got := cfg.DealCosts(7_958_145); got != 343_326 { // 318 325.8 → 318 326 + 25 000
		t.Errorf("DealCosts(7958145) = %d, want 343326 (округление)", got)
	}
	zero := Config{RealtorFeePct: 0, TitleInsurancePct: 0}
	if got := zero.DealCosts(10_000_000); got != 0 {
		t.Errorf("нулевые ставки: DealCosts = %d, want 0", got)
	}
}

// Медиана/перцентили кластера (линейная интерполяция, как numpy).
func TestClusterStats(t *testing.T) {
	one := func(p int) *int { return &p }
	comps := []Listing{
		{Price: 26000, TotalArea: 21.2, Studio: true, Rooms: one(0), Address: "x"},
		{Price: 27000, TotalArea: 20.9, Studio: true, Rooms: one(0), Address: "x"},
		{Price: 28000, TotalArea: 21.5, Studio: true, Rooms: one(0), Address: "x"},
		{Price: 32000, TotalArea: 22.0, Studio: true, Rooms: one(0), Address: "x"},
	}
	input := Listing{TotalArea: 21.2, Studio: true, Address: "x"}
	c := BuildCluster(comps, &input)
	s := c.Stats()
	if s.N != 4 {
		t.Fatalf("N = %d", s.N)
	}
	approx(t, "median", s.Median, 27500, 1e-9)
	approx(t, "p25", s.P25, 26750, 1e-9)
	approx(t, "p75", s.P75, 29000, 1e-9)
	approx(t, "min", s.Min, 26000, 1e-9)
	approx(t, "max", s.Max, 32000, 1e-9)
}

// Кластер: только студии того же дома в диапазоне площади ±20%.
func TestBuildClusterFilters(t *testing.T) {
	one := func(p int) *int { return &p }
	mk := func(id int64, house, title string, area float64, studio bool) Listing {
		l := Listing{ID: id, Price: 30000, TotalArea: area, Rooms: one(0), Studio: studio, Address: house, Title: title}
		return l
	}
	input := Listing{TotalArea: 21.2, Studio: true, Address: "same"}
	input.Geo = new(Geo)
	input.Geo.AddressLinks.HouseLink.Link = "/h/x/175634"

	comps := []Listing{
		mk(1, "same", "студия", 21.0, true),        // в кластере
		mk(2, "same", "студия", 34.0, true),        // площадь не фильтруется — тоже в кластере
		mk(3, "other", "студия", 21.0, true),       // другой дом
		mk(4, "same", "1-к квартира", 21.0, false), // не студия
		mk(5, "same", "студия", 21.4, true),        // в кластере
	}
	// mk ставит всем rooms=0; у «1-к» rooms=1, иначе она прошла бы как студия
	comps[3].Rooms = one(1)
	// comps 1,2,3 имеют адрес-дом; у 4/5 адрес тоже нужен как house key
	for i := range comps {
		if comps[i].Address == "same" || comps[i].Address == "other" {
			comps[i].Geo = new(Geo)
			id := "175634"
			if comps[i].Address == "other" {
				id = "999"
			}
			comps[i].Geo.AddressLinks.HouseLink.Link = "/h/x/" + id
		}
	}
	c := BuildCluster(comps, &input)
	if len(c.Comps) != 3 {
		t.Fatalf("в кластере %d аналогов, want 3 (ids 1, 2, 5)", len(c.Comps))
	}
	if c.Comps[0].ID != 1 || c.Comps[1].ID != 2 || c.Comps[2].ID != 5 {
		t.Fatalf("лишние аналоги: %d,%d,%d", c.Comps[0].ID, c.Comps[1].ID, c.Comps[2].ID)
	}
}

// Соседние здания без общих ключей в кластер не попадают, даже если
// координаты близко (радиус-фолбэк удалён по решению владельца).
func TestNoCrossZhkMatching(t *testing.T) {
	in := Listing{Studio: true, TotalArea: 24, Address: "Кубинская ул., 76к7"}
	near := Listing{Studio: true, TotalArea: 25, Price: 40000, Address: "Кубинская ул., 76к1"}
	cl := BuildCluster([]Listing{near}, &in)
	if len(cl.Comps) != 0 {
		t.Fatalf("аренда соседнего корпуса другого ЖК не должна попадать в кластер")
	}
}
