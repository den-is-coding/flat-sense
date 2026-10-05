// Package pipeline — обработка потока parsed-ads в analyzer-service
// (issue #6, событийный MVP-поток):
//
//   - parsed_ad — прогнать оценку окупаемости (internal/evaluate) по
//     avito_id объявления (данные уже в avito_listings — парсер пишет
//     их раньше события), записать результат в ad_roi_results (как
//     backfill #66) и уведомить downstream — gRPC AdsService.UpdateAnalysis
//     (ads-service сохранит срез в analysis_results);
//   - parse_error — конечная ошибка парсинга: считать нечего, лог + ack.
//
// Идемпотентность: дубли event_id отсекает консьюмер pkg/kafka, а
// upsert в ad_roi_results делает повторную обработку безопасной.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	pkgkafka "github.com/yourusername/real-estate-analyzer/pkg/kafka"
	analyzerv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/analyzer/v1"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/analysis"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/backfill"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// Evaluator — расчёт оценки объявления (реализует *evaluate.Evaluator).
type Evaluator interface {
	EvaluateByID(ctx context.Context, id int64) (*evaluate.Report, error)
}

// Saver — запись строки кэша ad_roi_results (реализует *backfill.Runner
// после экспорта Upsert; в тестах — фейк).
type Saver interface {
	Upsert(ctx context.Context, row backfill.Row) error
}

// Notifier — уведомление downstream о готовом анализе (реализует
// *AdsNotifier; nil — downstream отключён, поток не ломается).
type Notifier interface {
	NotifyAnalysis(ctx context.Context, res *analyzerv1.AnalysisResult) error
}

// Handler — kafka.Handler консьюмера топика parsed-ads (group
// analyzer-service).
type Handler struct {
	ev       Evaluator
	saver    Saver
	notifier Notifier
	cfg      evaluate.Config
	log      *slog.Logger

	// parseErrors — счётчик полученных parse_error (подобие метрики,
	// как у консьюмера ads-service).
	parseErrors atomic.Int64
}

// NewHandler — обработчик потока. notifier может быть nil (ADS_GRPC_ADDR
// пуст): считаем и пишем ad_roi_results, downstream не уведомляем.
func NewHandler(ev Evaluator, saver Saver, notifier Notifier, cfg evaluate.Config, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{ev: ev, saver: saver, notifier: notifier, cfg: cfg, log: log}
}

// ParseErrorsTotal — сколько parse_error получено с старта сервиса.
func (h *Handler) ParseErrorsTotal() int64 { return h.parseErrors.Load() }

// HandleMessage — декодирует payload по типу конверта. Ошибка
// декодирования или чужой тип события возвращается наружу: консьюмер
// pkg/kafka повторит попытки и уложит сообщение в parsed-ads-dlq.
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
		return fmt.Errorf("pipeline: неожиданный тип события %q в топике %s", m.Envelope.Type, m.Topic)
	}
}

// handleParsedAd — оценка объявления: evaluate → ad_roi_results →
// уведомление ads-service.
//
//   - считаются только объявления-продажи (как в backfill #66 и
//     контракте EvaluateAd «оценка объявления-продажи»): аренда в
//     окупаемость покупки не превращается — не-продажи ack-аются без
//     расчёта (их данные и так в арендном пуле avito_listings);
//   - ошибки evaluate (объявления нет в avito_listings, недоступна БД)
//     возвращаются наружу: at-least-once повторит доставку, повторный
//     upsert безопасен; после maxAttempts консьюмер уведёт сообщение
//     в parsed-ads-dlq;
//   - ошибка уведомления ads-service поток НЕ ломает (issue #6):
//     результат уже сохранён в ad_roi_results — лог-ошибка и ack.
func (h *Handler) handleParsedAd(ctx context.Context, ev pkgkafka.ParsedAd) error {
	if ev.Ad == nil || ev.Ad.GetId() == 0 {
		return fmt.Errorf("pipeline: parsed_ad без объявления (ad_id = 0, request_id = %q)", ev.RequestID)
	}
	adID := ev.Ad.GetId()
	if dt := ev.Ad.GetDealType(); dt != "" && dt != "sale" {
		h.log.Info("parsed-ads: объявление не продажа — окупаемость не считается, ack",
			"ad_id", adID, "deal_type", dt, "request_id", ev.RequestID)
		return nil
	}

	rep, err := h.ev.EvaluateByID(ctx, adID)
	if err != nil {
		return fmt.Errorf("pipeline: evaluate %d: %w", adID, err)
	}

	// Тот же маппинг, что у backfill #66: кэш и downstream не расходятся.
	row := backfill.RowFromReport(rep, h.cfg)
	row.AdID = adID
	if err := h.saver.Upsert(ctx, row); err != nil {
		return fmt.Errorf("pipeline: upsert ad_roi_results %d: %w", adID, err)
	}

	res := analysis.ResultFromRow(row)
	if h.notifier != nil {
		if err := h.notifier.NotifyAnalysis(ctx, res); err != nil {
			h.log.Error("parsed-ads: ads-service не подтвердил UpdateAnalysis после ретраев — результат остался в ad_roi_results, поток продолжается",
				"ad_id", adID, "request_id", ev.RequestID, "err", err)
		}
	}
	h.log.Info("parsed-ads: оценка окупаемости записана",
		"ad_id", adID, "request_id", ev.RequestID, "status", row.Status,
		"confidence", row.Confidence, "cluster_n", analysis.ClusterNOf(row))
	return nil
}

// handleParseError — конечная ошибка парсинга: оценивать нечего — лог,
// счётчик и ack (возврат nil коммитит offset, ретраи не нужны).
func (h *Handler) handleParseError(ev pkgkafka.ParseError) error {
	total := h.parseErrors.Add(1)
	h.log.Warn("parsed-ads: получен parse_error (оценивать нечего)",
		"request_id", ev.RequestID, "error", ev.Error, "parse_errors_total", total)
	return nil
}
