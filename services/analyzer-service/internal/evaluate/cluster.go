package evaluate

import (
	"fmt"
	"sort"
)

// RentStats — сводка арендного кластера (цены в рублях/мес).
type RentStats struct {
	N            int     `json:"n"`            // всего аналогов в кластере
	NFurnished   int     `json:"nFurnished"`   // из них с мебелью
	NUnfurnished int     `json:"nUnfurnished"` // без мебели
	Median       float64 `json:"median"`       // медиана кластера целиком
	P25          float64 `json:"p25"`
	P75          float64 `json:"p75"`
	Min          float64 `json:"min"`
	Max          float64 `json:"max"`
}

// Cluster — арендный кластер: студии того же дома (или адреса) в
// диапазоне площади входного объявления (правила #55: ЖК/дом → комнаты →
// площадь; параметры диапазона — в Config, см. README).
type Cluster struct {
	HouseKey  string    `json:"houseKey"`
	AreaMin   float64   `json:"areaMin"` // границы диапазона площади
	AreaMax   float64   `json:"areaMax"`
	Comps     []Listing `json:"-"`
	Furnished []Listing `json:"-"`
	Unfurn    []Listing `json:"-"`
}

// BuildCluster отбирает из comps студии того же дома в диапазоне площади
// и разбивает их по мебели (неопределённые остаются только в Comps —
// в строгие группы сценариев они не попадают).
func BuildCluster(comps []Listing, input *Listing, areaTolPct float64) Cluster {
	lo := input.TotalArea * (1 - areaTolPct/100)
	hi := input.TotalArea * (1 + areaTolPct/100)
	c := Cluster{HouseKey: input.HouseKey(), AreaMin: lo, AreaMax: hi}
	for i := range comps {
		r := &comps[i]
		if !r.IsStudio() || r.Price <= 0 {
			continue
		}
		if r.TotalArea < lo || r.TotalArea > hi {
			continue
		}
		if r.HouseKey() != c.HouseKey {
			continue
		}
		c.Comps = append(c.Comps, *r)
		f, _ := DetectFurnishing(r.Description, r.Params)
		switch f {
		case Furnished:
			c.Furnished = append(c.Furnished, *r)
		case Unfurnished:
			c.Unfurn = append(c.Unfurn, *r)
		}
	}
	return c
}

// Stats — сводка по кластеру.
func (c *Cluster) Stats() RentStats {
	s := RentStats{N: len(c.Comps), NFurnished: len(c.Furnished), NUnfurnished: len(c.Unfurn)}
	if s.N == 0 {
		return s
	}
	prices := make([]float64, 0, s.N)
	for i := range c.Comps {
		prices = append(prices, float64(c.Comps[i].Price))
	}
	sort.Float64s(prices)
	s.Median = quantile(prices, 0.5)
	s.P25 = quantile(prices, 0.25)
	s.P75 = quantile(prices, 0.75)
	s.Min = prices[0]
	s.Max = prices[len(prices)-1]
	return s
}

// groupMedian — медиана цены строгой группы (furnished/unfurnished).
func (c *Cluster) groupMedian(l []Listing) (float64, bool) {
	if len(l) == 0 {
		return 0, false
	}
	prices := make([]float64, 0, len(l))
	for i := range l {
		prices = append(prices, float64(l[i].Price))
	}
	sort.Float64s(prices)
	return quantile(prices, 0.5), true
}

// quantile — перцентиль с линейной интерполяцией (как в Excel/numpy).
func quantile(sorted []float64, q float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	pos := q * float64(n-1)
	lo := int(pos)
	hi := lo + 1
	if hi >= n {
		return sorted[n-1]
	}
	return sorted[lo] + (pos-float64(lo))*(sorted[hi]-sorted[lo])
}

// Scenario — одна строка сценария оценки.
type Scenario struct {
	Name           string  `json:"name"` // «без мебели» | «с мебелью»
	Applicable     bool    `json:"applicable"`
	SkippedReason  string  `json:"skippedReason,omitempty"`
	RentMedian     float64 `json:"rentMedian"` // ₽/мес
	RentP25        float64 `json:"rentP25"`
	RentP75        float64 `json:"rentP75"`
	Comps          int     `json:"comps"`          // число аналогов сценария
	PriceUsed      int64   `json:"priceUsed"`      // цена для расчёта
	FurnishingCost int64   `json:"furnishingCost"` // надбавка на мебель (0 — без неё)
	PaybackYears   float64 `json:"paybackYears"`   // окупаемость, лет
	YieldPct       float64 `json:"yieldPct"`       // доходность, % в год
}

// ComputeScenario — арифметика одного сценария (чистая функция, тестируется
// table-driven): аренда = медиана группы; окупаемость = цена / (аренда × 12);
// доходность = аренда × 12 / цена × 100%.
func ComputeScenario(name string, rentMedian, rentP25, rentP75 float64, comps int, price, furnishingCost int64) Scenario {
	priceUsed := price + furnishingCost
	s := Scenario{
		Name:           name,
		Applicable:     comps > 0 && rentMedian > 0 && priceUsed > 0,
		RentMedian:     rentMedian,
		RentP25:        rentP25,
		RentP75:        rentP75,
		Comps:          comps,
		PriceUsed:      priceUsed,
		FurnishingCost: furnishingCost,
	}
	if !s.Applicable {
		s.SkippedReason = "нет аналогов с определённой мебелью в кластере"
		return s
	}
	annual := rentMedian * 12
	s.PaybackYears = float64(priceUsed) / annual
	s.YieldPct = annual / float64(priceUsed) * 100
	return s
}

// warningMinCluster — предупреждение о малом кластере.
func warningMinCluster(n, min int) string {
	return fmt.Sprintf("в кластере мало аналогов (%d < %d) — оценка ненадёжна", n, min)
}
