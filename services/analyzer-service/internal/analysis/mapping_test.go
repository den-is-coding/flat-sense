package analysis

import (
	"testing"

	analyzerv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/analyzer/v1"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/backfill"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// okReport — отчёт «без мебели» с двумя применимыми сценариями
// (по образцу фикстур backfill_test). Цена 7_600_000: издержки сделки
// 4%+25к = 329_000 (риэлтор 228_000 + титул/оформление 101_000);
// PriceUsed «с мебелью» больше ровно на надбавку 500_000.
func okReport() *evaluate.Report {
	return &evaluate.Report{
		Status:          "ok",
		InputFurnishing: evaluate.Unfurnished,
		Confidence:      "low",
		Notice:          "внимание: …",
		Listing:         &evaluate.ListingView{ID: 7907741579, Price: 7_600_000},
		Cluster:         &evaluate.ClusterView{N: 2, NFurnished: 1, NUnfurnished: 1},
		Scenarios: []evaluate.Scenario{
			{Name: "без мебели", Applicable: true, YieldPct: 4.9, PriceUsed: 7_929_000, RentMedian: 32_000, RentP25: 31_000, RentP75: 33_000, Comps: 1, DealCosts: 329_000},
			{Name: "с мебелью (после меблировки)", Applicable: true, YieldPct: 4.6, PriceUsed: 8_429_000, RentMedian: 32_000, RentP25: 31_000, RentP75: 33_000, Comps: 1, FurnishingCost: 500_000, DealCosts: 329_000},
		},
	}
}

// TestResultFromReport_Ok — отчёт → контракт: сценарии, издержки,
// методика. Издержки считаются RowFromReport (одна формула с кэшем).
func TestResultFromReport_Ok(t *testing.T) {
	res := ResultFromReport(okReport(), evaluate.DefaultConfig(), 7907741579)

	if res.AdId != 7907741579 || res.Status != "ok" {
		t.Fatalf("ad/status = %d/%q", res.AdId, res.Status)
	}
	if res.Furnishing != "unfurnished" {
		t.Fatalf("furnishing = %q", res.Furnishing)
	}
	if res.ClusterN != 2 || res.Confidence != "low" {
		t.Fatalf("cluster_n/confidence = %d/%q", res.ClusterN, res.Confidence)
	}
	if !res.GetUnfurnished().GetApplicable() {
		t.Fatalf("unfurnished должен быть применим: %v", res.Unfurnished)
	}
	if res.GetUnfurnished().GetYieldPct() != 4.9 ||
		res.GetUnfurnished().GetRentMedian() != 32_000 ||
		res.GetUnfurnished().GetFullCost() != 7_929_000 ||
		res.GetUnfurnished().GetComps() != 1 {
		t.Fatalf("unfurnished = %v", res.Unfurnished)
	}
	if !res.GetFurnished().GetApplicable() {
		t.Fatalf("furnished должен быть применим: %v", res.Furnished)
	}
	// Надбавка на меблировку: full_cost furnished > unfurnished ровно на 500к.
	if d := res.GetFurnished().GetFullCost() - res.GetUnfurnished().GetFullCost(); d != 500_000 {
		t.Fatalf("разница full_cost = %d, want 500000 (надбавка)", d)
	}
	if res.GetFurnished().GetFurnishingCost() != 500_000 {
		t.Fatalf("furnishing_cost = %d", res.GetFurnished().GetFurnishingCost())
	}
	// Издержки сделки: 4% от цены + 25к фикс = 329_000,
	// риэлтор 3% = 228_000, остальное — титул+оформление.
	if res.DealCostsTotal != 329_000 || res.RealtorFee != 228_000 || res.DealCostsOther != 101_000 {
		t.Fatalf("deal costs: total=%d realtor=%d other=%d",
			res.DealCostsTotal, res.RealtorFee, res.DealCostsOther)
	}
	if res.ComputedAt == nil || res.ComputedAt.AsTime().IsZero() {
		t.Fatalf("computed_at не заполнен")
	}
}

// TestResultFromReport_NoRentData — валидный исход «нет арендных данных»:
// статус и методика есть, сценарии applicable=false (не нули).
func TestResultFromReport_NoRentData(t *testing.T) {
	rep := &evaluate.Report{
		Status:          "no_rent_data",
		Notice:          "по дому/ЖК addr:… нет арендных данных",
		InputFurnishing: evaluate.Unfurnished,
	}
	res := ResultFromReport(rep, evaluate.DefaultConfig(), 42)
	if res.Status != "no_rent_data" {
		t.Fatalf("status = %q", res.Status)
	}
	if res.GetUnfurnished().GetApplicable() || res.GetFurnished().GetApplicable() {
		t.Fatalf("сценарии должны быть неприменимы: %v / %v", res.Unfurnished, res.Furnished)
	}
	if res.ClusterN != 0 || res.DealCostsTotal != 0 {
		t.Fatalf("методика/издержки должны быть нулевыми: %v", res)
	}
}

// TestResultFromRow_RowFromResult — двусторонний круговорот
// Row → proto → Row сохраняет данные записи кэша.
func TestResultFromRow_RowFromResult(t *testing.T) {
	rep := okReport()
	row := backfill.RowFromReport(rep, evaluate.DefaultConfig())
	row.AdID = 7907741579

	res := ResultFromRow(row)
	back := RowFromResult(res)

	if back.AdID != row.AdID || back.Status != row.Status ||
		back.InputFurnishing != row.InputFurnishing ||
		back.Confidence != row.Confidence {
		t.Fatalf("шапка не совпала: %+v vs %+v", back, row)
	}
	if !eqPtr64(back.YieldUnfurnished, row.YieldUnfurnished) ||
		!eqPtr64(back.RentMedianUnfurn, row.RentMedianUnfurn) ||
		!eqPtri64(back.TotalCostUnfurn, row.TotalCostUnfurn) ||
		!eqPtrInt(back.CompsUnfurn, row.CompsUnfurn) {
		t.Fatalf("сценарий «без мебели» не совпал: %+v vs %+v", back, row)
	}
	if !eqPtr64(back.YieldFurnished, row.YieldFurnished) ||
		!eqPtri64(back.TotalCostFurn, row.TotalCostFurn) ||
		!eqPtri64(back.FurnishingCost, row.FurnishingCost) {
		t.Fatalf("сценарий «с мебелью» не совпал: %+v vs %+v", back, row)
	}
	if !eqPtri64(back.RealtorFee, row.RealtorFee) ||
		!eqPtri64(back.DealCostsOther, row.DealCostsOther) ||
		!eqPtri64(back.DealCostsTotal, row.DealCostsTotal) ||
		!eqPtrInt(back.ClusterN, row.ClusterN) {
		t.Fatalf("издержки/методика не совпали: %+v vs %+v", back, row)
	}
}

// TestRowFromResult_InapplicableScenarios — applicable=false → все
// сценарные колонки nil (NULL в ad_roi_results, не ноль).
func TestRowFromResult_InapplicableScenarios(t *testing.T) {
	res := &analyzerv1.AnalysisResult{
		AdId:        42,
		Status:      "no_rent_data",
		Furnishing:  "unknown",
		Unfurnished: &analyzerv1.Scenario{Applicable: false, YieldPct: 0},
		Furnished:   &analyzerv1.Scenario{Applicable: false},
	}
	row := RowFromResult(res)
	if row.YieldUnfurnished != nil || row.YieldFurnished != nil ||
		row.RentMedianUnfurn != nil || row.RentMedianFurn != nil ||
		row.CompsUnfurn != nil || row.CompsFurn != nil ||
		row.TotalCostUnfurn != nil || row.TotalCostFurn != nil {
		t.Fatalf("неприменимые сценарии должны давать NULL: %+v", row)
	}
	if row.AdID != 42 || row.Status != "no_rent_data" || row.InputFurnishing != "unknown" {
		t.Fatalf("шапка: %+v", row)
	}
}

// TestRowFromResult_NilScenario — nil-сценарии в proto (не заданы) тоже
// допустимы и дают NULL-колонки.
func TestRowFromResult_NilScenario(t *testing.T) {
	row := RowFromResult(&analyzerv1.AnalysisResult{AdId: 7, Status: "ok"})
	if row.YieldUnfurnished != nil || row.YieldFurnished != nil {
		t.Fatalf("nil-сценарии должны давать NULL: %+v", row)
	}
}

func eqPtr64(a, b *float64) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a != nil && b != nil:
		return *a == *b
	}
	return false
}

func eqPtri64(a, b *int64) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a != nil && b != nil:
		return *a == *b
	}
	return false
}

func eqPtrInt(a, b *int) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a != nil && b != nil:
		return *a == *b
	}
	return false
}
