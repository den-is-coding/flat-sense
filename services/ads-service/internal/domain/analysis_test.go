package domain

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	analyzerv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/analyzer/v1"
)

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestAnalysisFromProtoFurnishedPreferred(t *testing.T) {
	computed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	res := &analyzerv1.AnalysisResult{
		AdId:   777,
		Status: "ok",
		Unfurnished: &analyzerv1.Scenario{
			Applicable: true, RentMedian: 40000, YieldPct: 3.5, PaybackYears: 28.5,
		},
		Furnished: &analyzerv1.Scenario{
			Applicable: true, RentMedian: 45000, YieldPct: 4.1, PaybackYears: 24.4,
		},
		ComputedAt: timestamppb.New(computed),
	}
	a, err := AnalysisFromProto(res)
	if err != nil {
		t.Fatalf("AnalysisFromProto: %v", err)
	}
	// Применимый сценарий «с мебелью» приоритетнее.
	if a.RentForecast == nil || *a.RentForecast != 45000 {
		t.Errorf("RentForecast = %v, хочу 45000", a.RentForecast)
	}
	if a.YieldPercent == nil || *a.YieldPercent != 4.1 {
		t.Errorf("YieldPercent = %v, хочу 4.1", a.YieldPercent)
	}
	if a.PaybackYears == nil || *a.PaybackYears != 24.4 {
		t.Errorf("PaybackYears = %v, хочу 24.4", a.PaybackYears)
	}
	if !a.ComputedAt.Equal(computed) {
		t.Errorf("ComputedAt = %v, хочу %v", a.ComputedAt, computed)
	}
	assertPayload(t, a, "ok")
}

// Нет арендных данных по «с мебели» — берём сценарий «без мебели».
func TestAnalysisFromProtoUnfurnishedFallback(t *testing.T) {
	res := &analyzerv1.AnalysisResult{
		AdId:        778,
		Status:      "ok",
		Unfurnished: &analyzerv1.Scenario{Applicable: true, RentMedian: 38000, YieldPct: 3.2, PaybackYears: 31.1},
	}
	a, err := AnalysisFromProto(res)
	if err != nil {
		t.Fatalf("AnalysisFromProto: %v", err)
	}
	if a.RentForecast == nil || *a.RentForecast != 38000 {
		t.Errorf("RentForecast = %v, хочу 38000", a.RentForecast)
	}
	if a.YieldPercent == nil || *a.YieldPercent != 3.2 || a.PaybackYears == nil || *a.PaybackYears != 31.1 {
		t.Errorf("yield/payback = %v/%v", a.YieldPercent, a.PaybackYears)
	}
}

// no_rent_data: неприменим ни один сценарий — расчётные поля NULL.
func TestAnalysisFromProtoNoRentData(t *testing.T) {
	res := &analyzerv1.AnalysisResult{
		AdId:        779,
		Status:      "no_rent_data",
		Unfurnished: &analyzerv1.Scenario{Applicable: false},
		Furnished:   &analyzerv1.Scenario{Applicable: false},
	}
	a, err := AnalysisFromProto(res)
	if err != nil {
		t.Fatalf("AnalysisFromProto: %v", err)
	}
	if a.RentForecast != nil || a.YieldPercent != nil || a.PaybackYears != nil {
		t.Errorf("без арендных данных расчётные поля должны быть nil: %+v", a)
	}
	if a.Status != "no_rent_data" {
		t.Errorf("Status = %q", a.Status)
	}
	if a.ComputedAt.IsZero() {
		t.Error("ComputedAt должен заполниться текущим временем, если в контракте пусто")
	}
	assertPayload(t, a, "no_rent_data")
}

func TestAnalysisFromProtoErrors(t *testing.T) {
	if _, err := AnalysisFromProto(nil); err == nil {
		t.Error("nil результат должен давать ошибку")
	}
	if _, err := AnalysisFromProto(&analyzerv1.AnalysisResult{Status: "ok"}); err == nil {
		t.Error("результат без ad_id должен давать ошибку")
	}
}

// Payload — полный AnalysisResult в protojson: новые поля контракта
// читаются из payload без миграции.
func assertPayload(t *testing.T, a Analysis, wantStatus string) {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(a.Payload, &raw); err != nil {
		t.Fatalf("payload не json: %v", err)
	}
	if got, _ := raw["status"].(string); got != wantStatus {
		t.Errorf("payload.status = %q, хочу %q", got, wantStatus)
	}
	// protojson кодирует int64 строкой — допускаем обе формы.
	switch v := raw["adId"].(type) {
	case string:
		if v != itoa(a.AdID) {
			t.Errorf("payload.adId = %q, хочу %d", v, a.AdID)
		}
	case float64:
		if int64(v) != a.AdID {
			t.Errorf("payload.adId = %v, хочу %d", v, a.AdID)
		}
	default:
		t.Errorf("payload.adId отсутствует или неожиданного типа: %v", raw["adId"])
	}
	if !strings.Contains(string(a.Payload), "unfurnished") {
		t.Errorf("в payload потеряны сценарии: %s", a.Payload)
	}
}
