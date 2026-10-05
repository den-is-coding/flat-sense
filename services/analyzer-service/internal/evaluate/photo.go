package evaluate

import "context"

// PhotoFurnishingDetector — порт фото-детектора меблировки (issue #110,
// фото-фолбэк): применяется только когда текст не дал уверенного ответа.
// Приоритет источников: явный текст > фото > консервативный default.
// Реализация может быть недоступна (image-ai не отвечает) — вызывающая
// сторона обязана деградировать в текстовый режим, а не падать.
type PhotoFurnishingDetector interface {
	// DetectByPhotos возвращает агрегированный вердикт по фото объявления
	// и уверенность 0..1. Furnishing = furnished | unfurnished | unknown.
	DetectByPhotos(ctx context.Context, photoURLs []string) (Furnishing, float64, error)
}
