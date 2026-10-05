// Package consumer — обработка потока parsed-ads (issue #5):
//   - parsed_ad — upsert объявления в проекцию ads по avito_id;
//   - parse_error — конечная ошибка парсинга: лог + счётчик, ack
//     (сообщение коммитится, gateway пометит запрос failed).
package consumer

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	pkgkafka "github.com/yourusername/real-estate-analyzer/pkg/kafka"

	"github.com/yourusername/real-estate-analyzer/ads-service/internal/domain"
)

// AdSaver — часть domain.AdRepository, нужная консьюмеру
// (в тестах подменяется фейком).
type AdSaver interface {
	UpsertAd(ctx context.Context, ad domain.Ad) (created bool, err error)
}

// Handler — kafka.Handler консьюмера топика parsed-ads
// (group ads-service). Идемпотентность: дубли event_id отсекает
// консьюмер pkg/kafka, повторный upsert в БД безопасен.
type Handler struct {
	saver AdSaver
	log   *slog.Logger

	// parseErrors — счётчик полученных parse_error (подобие метрики:
	// выводится в каждой warn-строке; в будущем — на /metrics).
	parseErrors atomic.Int64
}

// NewHandler — обработчик поверх хранилища проекции.
func NewHandler(saver AdSaver, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{saver: saver, log: log}
}

// ParseErrorsTotal — сколько parse_error получено с старта сервиса.
func (h *Handler) ParseErrorsTotal() int64 { return h.parseErrors.Load() }

// HandleMessage — декодирует payload по типу конверта.
// Ошибка декодирования или чужой тип события возвращается наружу:
// консьюмер pkg/kafka повторит попытки и уложит сообщение в
// parsed-ads-dlq — такие сообщения повторами не лечатся.
func (h *Handler) HandleMessage(ctx context.Context, m pkgkafka.Message) error {
	switch m.Envelope.Type {
	case pkgkafka.EventTypeParsedAd:
		var ev pkgkafka.ParsedAd
		if err := m.DecodePayload(&ev); err != nil {
			return err
		}
		return h.handleParsedAd(ctx, ev)
	case pkgkafka.EventTypeParseError:
		var ev pkgkafka.ParseError
		if err := m.DecodePayload(&ev); err != nil {
			return err
		}
		return h.handleParseError(ev)
	default:
		return fmt.Errorf("consumer: неожиданный тип события %q в топике %s", m.Envelope.Type, m.Topic)
	}
}

// handleParsedAd — upsert объявления. Ошибка БД возвращается наружу:
// at-least-once повторит доставку, upsert делает повторы безопасными;
// после maxAttempts консьюмер уведёт сообщение в DLQ.
func (h *Handler) handleParsedAd(ctx context.Context, ev pkgkafka.ParsedAd) error {
	ad, err := domain.AdFromProto(ev.Ad)
	if err != nil {
		return fmt.Errorf("consumer: %w", err)
	}
	ad.RequestID = ev.RequestID

	created, err := h.saver.UpsertAd(ctx, ad)
	if err != nil {
		return fmt.Errorf("consumer: upsert ad %d: %w", ad.AvitoID, err)
	}
	h.log.Info("parsed-ads: объявление сохранено в проекцию",
		"avito_id", ad.AvitoID, "request_id", ev.RequestID,
		"created", created, "title", ad.Title)
	return nil
}

// handleParseError — конечная ошибка парсинга: не наше хранилище, а
// статус запроса (gateway пометит failed). Лог + счётчик и ack: возврат
// nil коммитит offset, ретраи не нужны.
func (h *Handler) handleParseError(ev pkgkafka.ParseError) error {
	total := h.parseErrors.Add(1)
	h.log.Warn("parsed-ads: получен parse_error (парсинг не удался)",
		"request_id", ev.RequestID, "error", ev.Error, "parse_errors_total", total)
	return nil
}
