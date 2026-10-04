// Package floorplan — детекция планировок (floor plans) среди фото
// объявления (issue #58). Порт Detector реализуется адаптерами:
// эвристика (HeuristicDetector) сейчас, ML-классификатор — при
// необходимости позже.
package floorplan

import "image"

// Method — имя адаптера, записывается в ad_floor_plans.method.
const (
	MethodHeuristic = "heuristic"
	MethodML        = "ml"
)

// Verdict — результат проверки одного фото.
type Verdict struct {
	IsPlan     bool    `json:"isPlan"`
	Confidence float64 `json:"confidence"` // 0..1, вероятность «планировка»
	Method     string  `json:"method"`     // heuristic | ml
}

// Detector — порт детекции планировки по изображению.
// Реализация должна быть потокобезопасной.
type Detector interface {
	Detect(img image.Image) Verdict
}
