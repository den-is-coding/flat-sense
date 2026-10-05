package kafka

import adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"

// Типы сообщений MVP-потока (issue #3). Поля в snake_case — это то,
// что реально видно в топике внутри Payload конверта.

// ParseRequest — задание парсеру (топик TopicParseRequests).
type ParseRequest struct {
	// RequestID — идемпотентный ключ запроса (партиционируется по нему).
	RequestID string `json:"request_id"`
	// URL — страница объявления или списка (что парсить).
	URL string `json:"url"`
	// Source — источник (пока "avito").
	Source string `json:"source"`
}

// ParsedAd — результат парсинга (топик TopicParsedAds).
type ParsedAd struct {
	// RequestID — тот же ключ, что в исходном ParseRequest.
	RequestID string `json:"request_id"`
	// Ad — объявление, контракт ads.v1.Ad (issue #2), срез avito_listings.
	Ad *adsv1.Ad `json:"ad"`
}

// ParseError — неуспешный результат парсинга (топик TopicParsedAds,
// issue #4). Публикуется, когда парсинг завершился конечной ошибкой
// (блокировка Авито, страница не найдена): получатель (gateway)
// помечает запрос failed. Это событие потока, а не DLQ: DLQ — для
// битых конвертов и необрабатываемых сообщений.
type ParseError struct {
	// RequestID — тот же ключ, что в исходном ParseRequest.
	RequestID string `json:"request_id"`
	// Error — человекочитаемое описание ошибки парсинга.
	Error string `json:"error"`
}
