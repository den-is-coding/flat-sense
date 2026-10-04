package backfill

import (
	"testing"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// noRent — отчёт «нет арендных данных»: все сценарные поля nil («—»).
func TestRowFromReport_NoRentData(t *testing.T) {
	rep := &evaluate.Report{
		Status:          "no_rent_data",
		Notice:          "по дому/ЖК addr:… нет арендных данных",
		InputFurnishing: evaluate.Unfurnished,
	}
	row := RowFromReport(rep, evaluate.DefaultConfig())
	if row.Status != "no_rent_data" {
		t.Fatalf("status = %q", row.Status)
	}
	if row.YieldUnfurnished != nil || row.YieldFurnished != nil ||
		row.TotalCostUnfurn != nil || row.TotalCostFurn != nil ||
		row.ClusterN != nil {
		t.Fatalf("no_rent_data must yield all-nil scenario fields: %+v", row)
	}
}

// furnishedInput — у меблированного объявления один сценарий «с мебелью»
// (без надбавки), «без мебели» остаётся nil.
func TestRowFromReport_FurnishedInput(t *testing.T) {
	cost := int64(7_880_000)
	yield := 5.4
	rep := &evaluate.Report{
		Status:          "ok",
		InputFurnishing: evaluate.Furnished,
		Confidence:      "medium",
		Cluster:         &evaluate.ClusterView{N: 7, NFurnished: 4},
		Scenarios: []evaluate.Scenario{
			{Name: "с мебелью", Applicable: true, YieldPct: yield, PriceUsed: cost, RentMedian: 35_500, Comps: 4},
		},
	}
	row := RowFromReport(rep, evaluate.DefaultConfig())
	if row.YieldFurnished == nil || *row.YieldFurnished != yield {
		t.Fatalf("yieldFurnished = %v", row.YieldFurnished)
	}
	if row.TotalCostFurn == nil || *row.TotalCostFurn != cost {
		t.Fatalf("totalCostFurn = %v", row.TotalCostFurn)
	}
	if row.YieldUnfurnished != nil || row.TotalCostUnfurn != nil {
		t.Fatalf("furnished input must not have unfurnished scenario: %+v", row)
	}
	if row.ClusterN == nil || *row.ClusterN != 7 || row.Confidence != "medium" {
		t.Fatalf("methodology fields: n=%v conf=%q", row.ClusterN, row.Confidence)
	}
}

// unfurnishedInput — оба сценария по своим группам.
func TestRowFromReport_UnfurnishedInput(t *testing.T) {
	rep := &evaluate.Report{
		Status:          "ok",
		InputFurnishing: evaluate.Unfurnished,
		Confidence:      "low",
		Cluster:         &evaluate.ClusterView{N: 2, NFurnished: 1, NUnfurnished: 1},
		Scenarios: []evaluate.Scenario{
			{Name: "без мебели", Applicable: true, YieldPct: 4.9, PriceUsed: 7_853_000, RentMedian: 32_000, Comps: 1},
			{Name: "с мебелью (после меблировки)", Applicable: true, YieldPct: 4.6, PriceUsed: 8_353_000, RentMedian: 32_000, Comps: 1},
		},
	}
	row := RowFromReport(rep, evaluate.DefaultConfig())
	if row.YieldUnfurnished == nil || *row.YieldUnfurnished != 4.9 {
		t.Fatalf("yieldUnfurnished = %v", row.YieldUnfurnished)
	}
	if row.TotalCostFurn == nil || *row.TotalCostFurn != 8_353_000 {
		t.Fatalf("totalCostFurn (с надбавкой) = %v", row.TotalCostFurn)
	}
	if row.TotalCostUnfurn == nil || *row.TotalCostUnfurn != 7_853_000 {
		t.Fatalf("totalCostUnfurn = %v", row.TotalCostUnfurn)
	}
}

// inapplicable — сценарий с нулевыми аналогами не попадает в строку
// как число (иначе в таблице был бы ложный 0.0 вместо «—»).
func TestRowFromReport_InapplicableScenario(t *testing.T) {
	rep := &evaluate.Report{
		Status:          "ok",
		InputFurnishing: evaluate.Unfurnished,
		Cluster:         &evaluate.ClusterView{N: 3},
		Scenarios: []evaluate.Scenario{
			{Name: "без мебели", Applicable: false, SkippedReason: "нет данных"},
			{Name: "с мебелью (после меблировки)", Applicable: false, SkippedReason: "нет данных"},
		},
	}
	row := RowFromReport(rep, evaluate.DefaultConfig())
	if row.YieldUnfurnished != nil || row.YieldFurnished != nil {
		t.Fatalf("inapplicable scenarios must stay nil: %+v", row)
	}
}

// Раскладка расходов (#108): риэлтор = % от цены, остальное — титул +
// оформление; меблировка — максимум по сценариям.
func TestRowFromReport_CostBreakdown(t *testing.T) {
	cfg := evaluate.DefaultConfig() // риэлтор 3 %, титул 1 %, оформление 25 000
	rep := &evaluate.Report{
		Status:          "ok",
		InputFurnishing: evaluate.Unfurnished,
		Confidence:      "low",
		Listing:         &evaluate.ListingView{ID: 1, Price: 7_500_000},
		Cluster:         &evaluate.ClusterView{N: 5},
		Scenarios: []evaluate.Scenario{
			{Name: "без мебели", Applicable: true, PriceUsed: 7_950_000, DealCosts: 325_000, FurnishingCost: 0},
			{Name: "с мебелью (после меблировки)", Applicable: true, PriceUsed: 8_450_000, DealCosts: 325_000, FurnishingCost: 500_000},
		},
	}
	row := RowFromReport(rep, cfg)
	if row.RealtorFee == nil || *row.RealtorFee != 225_000 {
		t.Fatalf("realtorFee = %v, want 225000", row.RealtorFee)
	}
	if row.DealCostsOther == nil || *row.DealCostsOther != 100_000 {
		t.Fatalf("dealCostsOther = %v, want 100000 (титул 1%% + оформление 25000)", row.DealCostsOther)
	}
	if row.DealCostsTotal == nil || *row.DealCostsTotal != 325_000 {
		t.Fatalf("dealCostsTotal = %v, want 325000", row.DealCostsTotal)
	}
	if row.FurnishingCost == nil || *row.FurnishingCost != 500_000 {
		t.Fatalf("furnishingCost = %v, want 500000", row.FurnishingCost)
	}
}
