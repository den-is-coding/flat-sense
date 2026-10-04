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
	row := RowFromReport(rep)
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
	row := RowFromReport(rep)
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
	row := RowFromReport(rep)
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
	row := RowFromReport(rep)
	if row.YieldUnfurnished != nil || row.YieldFurnished != nil {
		t.Fatalf("inapplicable scenarios must stay nil: %+v", row)
	}
}
