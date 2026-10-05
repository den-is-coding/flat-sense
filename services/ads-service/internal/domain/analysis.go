package domain

import (
	"errors"
	"fmt"
	"time"

	analyzerv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/analyzer/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

var (
	errNilAnalysis = errors.New("ads: пустой результат анализа")
	errNoAdID      = errors.New("ads: результат анализа без ad_id")
)

func errMarshalPayload(err error) error {
	return fmt.Errorf("ads: marshal analysis payload: %w", err)
}

// AnalysisFromProto — analyzer.v1.AnalysisResult → строка analysis_results.
//
// Минимальный срез «чтобы контракт был реализован» (issue #5):
//   - RentForecast — медиана аренды применимого сценария («с мебелью»
//     приоритетнее, иначе «без мебели»);
//   - YieldPercent / PaybackYears — одноимённые поля того же сценария
//     (считает analyzer-service, здесь только переложены);
//   - Payload — полный результат в protojson (обратная совместимость:
//     новые поля контракта читаются из payload без миграции);
//   - ComputedAt — из контракта, иначе текущее время UTC.
//
// Если неприменим ни один сценарий (нет арендных данных) — все три
// расчётных поля nil (в БД NULL), статус сохраняется как есть.
func AnalysisFromProto(res *analyzerv1.AnalysisResult) (Analysis, error) {
	if res == nil {
		return Analysis{}, errNilAnalysis
	}
	if res.GetAdId() == 0 {
		return Analysis{}, errNoAdID
	}

	a := Analysis{AdID: res.GetAdId(), Status: res.GetStatus()}
	if sc := applicableScenario(res); sc != nil {
		rent := sc.GetRentMedian()
		yield := sc.GetYieldPct()
		payback := sc.GetPaybackYears()
		a.RentForecast, a.YieldPercent, a.PaybackYears = &rent, &yield, &payback
	}

	payload, err := protojson.Marshal(res)
	if err != nil {
		return Analysis{}, errMarshalPayload(err)
	}
	a.Payload = payload

	if res.GetComputedAt() != nil {
		a.ComputedAt = res.GetComputedAt().AsTime()
	} else {
		a.ComputedAt = time.Now().UTC()
	}
	return a, nil
}

// applicableScenario — применимый сценарий расчёта: «с мебелью», иначе
// «без мебели», иначе nil.
func applicableScenario(res *analyzerv1.AnalysisResult) *analyzerv1.Scenario {
	if res.GetFurnished().GetApplicable() {
		return res.GetFurnished()
	}
	if res.GetUnfurnished().GetApplicable() {
		return res.GetUnfurnished()
	}
	return nil
}
