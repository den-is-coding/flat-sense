// AdsNotifier — downstream-уведомление ads-service (issue #6): готовый
// AnalysisResult уезжает в AdsService.UpdateAnalysis, чтобы срез лёг в
// analysis_results проекции ads-service (параллельно полной записи в
// ad_roi_results).
//
// Ретраи ограничены и живут ЗДЕСЬ, а не в консьюмере pkg/kafka: после
// N неудач Handler логирует ошибку и коммитит сообщение — поток не
// валим, результат уже сохранён в ad_roi_results.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"
	analyzerv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/analyzer/v1"
)

// notifyAttempts — попыток UpdateAnalysis на одно сообщение
// (ADS_NOTIFY_ATTEMPTS переопределяет).
const defaultNotifyAttempts = 3

// notifyBackoffBase — база экспоненциального backoff: 500мс, 1с, 2с, ...
const notifyBackoffBase = 500 * time.Millisecond

const notifyBackoffMax = 5 * time.Second

// AdsNotifier — gRPC-клиент AdsService с ограниченными ретаями.
type AdsNotifier struct {
	conn     *grpc.ClientConn
	stub     adsv1.AdsServiceClient
	attempts int
	backoff  func(attempt int) time.Duration
	log      *slog.Logger
}

// AdsNotifierOption — настройка нотификатора (для тестов).
type AdsNotifierOption func(*AdsNotifier)

// WithNotifyAttempts — число попыток UpdateAnalysis (>=1).
func WithNotifyAttempts(n int) AdsNotifierOption {
	return func(n2 *AdsNotifier) {
		if n > 0 {
			n2.attempts = n
		}
	}
}

// WithNotifyBackoff — своя функция паузы между попытками (тесты).
func WithNotifyBackoff(f func(attempt int) time.Duration) AdsNotifierOption {
	return func(n2 *AdsNotifier) {
		if f != nil {
			n2.backoff = f
		}
	}
}

// NewAdsNotifier — клиент к ads-service по адресу addr (ADS_GRPC_ADDR,
// в docker-compose — ads-service:50052). Соединение ленивое
// (grpc.NewClient): ads-service может подняться позже analyzer.
func NewAdsNotifier(addr string, log *slog.Logger, opts ...AdsNotifierOption) *AdsNotifier {
	if log == nil {
		log = slog.Default()
	}
	n := &AdsNotifier{
		attempts: defaultNotifyAttempts,
		backoff:  notifyBackoff,
		log:      log,
	}
	for _, opt := range opts {
		opt(n)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		// На практике NewClient ошибку не возвращает (ленивое
		// соединение); страховка на будущее — RPC просто упадёт.
		n.log.Error("analyzer: ads gRPC client", "addr", addr, "err", err)
	}
	n.conn = conn
	n.stub = adsv1.NewAdsServiceClient(conn)
	return n
}

// NotifyAnalysis — UpdateAnalysis с ограниченными ретраями. Возвращает
// последнюю ошибку после N неудач (решение «ack или нет» — за Handler).
func (n *AdsNotifier) NotifyAnalysis(ctx context.Context, res *analyzerv1.AnalysisResult) error {
	var lastErr error
	for attempt := 1; attempt <= n.attempts; attempt++ {
		if _, lastErr = n.stub.UpdateAnalysis(ctx, &adsv1.UpdateAnalysisRequest{Analysis: res}); lastErr == nil {
			if attempt > 1 {
				n.log.Warn("analyzer: ads-service принял UpdateAnalysis после ретрая",
					"ad_id", res.GetAdId(), "attempt", attempt)
			}
			return nil
		}
		if attempt < n.attempts {
			if err := sleep(ctx, n.backoff(attempt)); err != nil {
				return err // остановка сервиса
			}
		}
	}
	return fmt.Errorf("ads UpdateAnalysis после %d попыток: %w", n.attempts, lastErr)
}

// Close — освободить соединение (graceful shutdown).
func (n *AdsNotifier) Close() error {
	if n.conn == nil {
		return nil
	}
	return n.conn.Close()
}

// notifyBackoff — 500мс, 1с, 2с ... с потолком 5с.
func notifyBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := notifyBackoffBase << (attempt - 1)
	if d <= 0 || d > notifyBackoffMax {
		return notifyBackoffMax
	}
	return d
}

// sleep — пауза с отменой по ctx.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
