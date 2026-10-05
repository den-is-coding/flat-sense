// Package analysis — преобразования между тремя представлениями одного
// результата оценки (issue #6):
//
//	evaluate.Report (#64)  ─→  backfill.Row   (кэш ad_roi_results, 000014–16)
//	evaluate.Report (#64)  ─→  analyzer.v1.AnalysisResult (контракт #2)
//	analyzer.v1.AnalysisResult ─→ backfill.Row (gRPC UpdateAnalysis)
//
// Оба направления строятся от одной пары «отчёт → Row» (RowFromReport
// backfill), поэтому ad_roi_results и analysis_results не расходятся:
// в gRPC уходит ровно то, что записано в кэш.
package analysis

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	analyzerv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/analyzer/v1"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/backfill"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// ResultFromReport — отчёт #64 → контракт analyzer.v1. adID берётся из
// события parsed_ad (avito_id); computed_at — текущее время UTC.
func ResultFromReport(rep *evaluate.Report, cfg evaluate.Config, adID int64) *analyzerv1.AnalysisResult {
	row := backfill.RowFromReport(rep, cfg)
	row.AdID = adID
	return ResultFromRow(row)
}

// ResultFromRow — строка кэша ad_roi_results → контракт analyzer.v1.
// NULL-сценарии кэша → applicable=false (не нули, см. комментарий
// AnalysisResult в proto).
func ResultFromRow(row backfill.Row) *analyzerv1.AnalysisResult {
	res := &analyzerv1.AnalysisResult{
		AdId:        row.AdID,
		Status:      row.Status,
		Furnishing:  row.InputFurnishing,
		Confidence:  row.Confidence,
		Notice:      row.Notice,
		ComputedAt:  timestamppb.New(time.Now().UTC()),
		Unfurnished: scenarioFromRow(row.YieldUnfurnished, row.TotalCostUnfurn, row.RentMedianUnfurn, row.RentP25Unfurn, row.RentP75Unfurn, row.CompsUnfurn, nil),
		Furnished:   scenarioFromRow(row.YieldFurnished, row.TotalCostFurn, row.RentMedianFurn, row.RentP25Furn, row.RentP75Furn, row.CompsFurn, row.FurnishingCost),
	}
	res.RealtorFee = orZero64(row.RealtorFee)
	res.DealCostsOther = orZero64(row.DealCostsOther)
	res.DealCostsTotal = orZero64(row.DealCostsTotal)
	res.ClusterN = int32(orZeroInt(row.ClusterN))
	return res
}

// RowFromResult — контракт analyzer.v1 → строка кэша (обратное
// направление для gRPC UpdateAnalysis: результат, посчитанный другим
// путём, ложится в ad_roi_results тем же upsert, что и свой расчёт).
// Неприменимый сценарий (applicable=false) → NULL-колонки.
func RowFromResult(res *analyzerv1.AnalysisResult) backfill.Row {
	row := backfill.Row{
		AdID:            res.GetAdId(),
		Status:          res.GetStatus(),
		InputFurnishing: res.GetFurnishing(),
		Confidence:      res.GetConfidence(),
		Notice:          res.GetNotice(),
	}
	if n := res.GetClusterN(); n > 0 {
		c := int(n)
		row.ClusterN = &c
	}
	if v := res.GetRealtorFee(); v > 0 {
		row.RealtorFee = &v
	}
	if v := res.GetDealCostsOther(); v > 0 {
		row.DealCostsOther = &v
	}
	if v := res.GetDealCostsTotal(); v > 0 {
		row.DealCostsTotal = &v
	}
	row.YieldUnfurnished, row.TotalCostUnfurn, row.RentMedianUnfurn, row.RentP25Unfurn, row.RentP75Unfurn, row.CompsUnfurn = rowFromScenario(res.GetUnfurnished())
	row.YieldFurnished, row.TotalCostFurn, row.RentMedianFurn, row.RentP25Furn, row.RentP75Furn, row.CompsFurn = rowFromScenario(res.GetFurnished())
	if fc := res.GetFurnished().GetFurnishingCost(); fc > 0 && row.YieldFurnished != nil {
		row.FurnishingCost = &fc
	}
	return row
}

// scenarioFromRow — сценарные колонки кэша → analyzer.v1.Scenario.
// Нулевой указатель доходности/стоимости/медианы означает NULL в БД —
// сценарий неприменим. furnishingCost передаётся только для сценария
// «с мебелью».
func scenarioFromRow(yield *float64, totalCost *int64, median, p25, p75 *float64, comps *int, furnishingCost *int64) *analyzerv1.Scenario {
	s := &analyzerv1.Scenario{} // applicable=false — «сценарий неприменим»
	if yield == nil || totalCost == nil || median == nil {
		return s
	}
	s.Applicable = true
	s.YieldPct = *yield
	s.FullCost = *totalCost
	s.RentMedian = *median
	if p25 != nil {
		s.RentP25 = *p25
	}
	if p75 != nil {
		s.RentP75 = *p75
	}
	if comps != nil {
		s.Comps = int32(*comps)
	}
	if furnishingCost != nil {
		s.FurnishingCost = *furnishingCost
	}
	return s
}

// rowFromScenario — analyzer.v1.Scenario → сценарные колонки кэша
// (nil — сценарий неприменим, в БД NULL).
func rowFromScenario(s *analyzerv1.Scenario) (yield *float64, totalCost *int64, median, p25, p75 *float64, comps *int) {
	if s == nil || !s.GetApplicable() || s.GetRentMedian() <= 0 {
		return nil, nil, nil, nil, nil, nil
	}
	yield = &s.YieldPct
	totalCost = &s.FullCost
	median = &s.RentMedian
	if s.GetRentP25() > 0 {
		p25 = &s.RentP25
	}
	if s.GetRentP75() > 0 {
		p75 = &s.RentP75
	}
	if s.GetComps() > 0 {
		c := int(s.GetComps())
		comps = &c
	}
	return yield, totalCost, median, p25, p75, comps
}

// ClusterNOf — cluster_n строки кэша для логов (0 — NULL).
func ClusterNOf(row backfill.Row) int {
	if row.ClusterN == nil {
		return 0
	}
	return *row.ClusterN
}

func orZero64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func orZeroInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
