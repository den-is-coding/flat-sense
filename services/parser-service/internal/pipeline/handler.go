// Package pipeline связывает Kafka-поток (issue #4) с парсером Авито:
// консьюмер топика parse-requests запускает парсинг карточки, а результат
// — объявление или конечная ошибка — публикуется в parsed-ads.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/yourusername/real-estate-analyzer/parser-service/internal/avito"
	"github.com/yourusername/real-estate-analyzer/pkg/kafka"
)

// SourceAvito — единственный поддержанный источник ParseRequest.
const SourceAvito = "avito"

// ItemParser — порт парсинга одной карточки объявления (реализует
// *avito.Service; в тестах подменяется фейком).
type ItemParser interface {
	ParseOne(ctx context.Context, itemURL string) (*avito.Listing, error)
}

// EventPublisher — порт публикации событий потока (реализует
// *kafka.Producer; в тестах подменяется фейком).
type EventPublisher interface {
	PublishParsedAd(ctx context.Context, ad kafka.ParsedAd) error
	PublishParseError(ctx context.Context, e kafka.ParseError) error
}

// Handler — обработка ParseRequest из топика parse-requests
// (consumer group parser-service). Идемпотентность: дубли event_id
// отсекает консьюмер pkg/kafka, повторный парсинг того же URL
// безопасен благодаря upsert в avito_listings.
type Handler struct {
	parser    ItemParser
	publisher EventPublisher
	log       *slog.Logger
}

// NewHandler — обработчик поверх парсера и публикатора событий.
func NewHandler(parser ItemParser, publisher EventPublisher, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{parser: parser, publisher: publisher, log: log}
}

// HandleMessage — kafka.Handler консьюмера: декодирует payload.
// Ошибка декодирования (и чужой тип события) возвращается наружу —
// такие сообщения консьюмер pkg/kafka уложит в parse-requests-dlq,
// их повторами не вылечить.
func (h *Handler) HandleMessage(ctx context.Context, m kafka.Message) error {
	if m.Envelope.Type != kafka.EventTypeParseRequest {
		return fmt.Errorf("pipeline: неожиданный тип события %q в топике %s", m.Envelope.Type, m.Topic)
	}
	var req kafka.ParseRequest
	if err := m.DecodePayload(&req); err != nil {
		return err
	}
	return h.HandleRequest(ctx, req)
}

// HandleRequest — обработка одного ParseRequest:
//   - успешный парсинг → parsed-ads: parsed_ad (ads.v1.Ad, id — из БД
//     avito_listings после upsert);
//   - конечная ошибка (блокировка Авито, 404, пустой url) → parsed-ads:
//     parse_error и ack — парсинг не крутится ретраями, gateway отдаст
//     статус failed; ErrBlocked/ErrNotFound обрабатываются так же;
//   - ошибка публикации → ошибка наружу: консьюмер повторит попытку
//     (at-least-once; повторный upsert в БД делает это безопасным);
//   - остановка сервиса посреди обработки → ошибка наружу без
//     публикации: сообщение придёт заново после рестарта.
func (h *Handler) HandleRequest(ctx context.Context, req kafka.ParseRequest) error {
	switch {
	case strings.TrimSpace(req.URL) == "":
		return h.publishFailure(ctx, req, "parse request без url")
	case req.Source != "" && req.Source != SourceAvito:
		return h.publishFailure(ctx, req, fmt.Sprintf("source %q не поддерживается (жду %q)", req.Source, SourceAvito))
	}

	listing, err := h.parser.ParseOne(ctx, req.URL)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return h.publishFailure(ctx, req, err.Error())
	}
	if listing == nil || listing.ID == 0 {
		return h.publishFailure(ctx, req, "парсер вернул объявление без id")
	}

	ad := AdFromListing(listing)
	if err := h.publisher.PublishParsedAd(ctx, kafka.ParsedAd{
		RequestID: req.RequestID,
		Ad:        ad,
	}); err != nil {
		return fmt.Errorf("pipeline: publish parsed_ad: %w", err)
	}
	h.log.Info("parsed-ads: объявление опубликовано",
		"request_id", req.RequestID, "ad_id", ad.Id, "url", req.URL, "is_new", listing.IsNew)
	return nil
}

// publishFailure — конечная ошибка парсинга: событие parse_error в
// parsed-ads (gateway пометит запрос failed) и nil наружу — сообщение
// коммитится, ретраи не запускаются.
func (h *Handler) publishFailure(ctx context.Context, req kafka.ParseRequest, cause string) error {
	if err := h.publisher.PublishParseError(ctx, kafka.ParseError{
		RequestID: req.RequestID,
		Error:     cause,
	}); err != nil {
		return fmt.Errorf("pipeline: publish parse_error: %w", err)
	}
	h.log.Warn("parsed-ads: парсинг завершился ошибкой",
		"request_id", req.RequestID, "url", req.URL, "error", cause)
	return nil
}
