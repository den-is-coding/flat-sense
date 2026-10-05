//go:build integration

// Интеграционный тест событийного потока analyzer-service (issue #6)
// против живого стека: Kafka (parsed-ads) + PostgreSQL (avito_listings,
// ad_roi_results, опционально analysis_results ads-service). В обычный
// `go test ./...` не входит:
//
//	go test -tags integration -run TestPipeline ./internal/pipeline/
//
// Требует TEST_DSN (postgres с миграциями) и KAFKA_BROKERS; реклама
// ads-service (ADS_GRPC_ADDR) необязательна: если она недоступна,
// assertion по analysis_results пропускается (сервис сам переживает
// недоступность downstream — это часть проверяемого поведения).
package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pkgkafka "github.com/yourusername/real-estate-analyzer/pkg/kafka"
	adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/backfill"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/source"
)

const (
	itSaleID = 9002000001
	itRent1  = 9002000002
	itRent2  = 9002000003
)

func requireKafka(t *testing.T) pkgkafka.Config {
	t.Helper()
	cfg := pkgkafka.ConfigFromEnv()
	conn, err := kafka.Dial("tcp", cfg.Brokers[0])
	if err != nil {
		t.Skipf("Kafka недоступна на %s: %v (docker compose -f deploy/docker-compose.yml up -d kafka)", cfg.Brokers[0], err)
	}
	_ = conn.Close()
	return cfg
}

func requireDSN(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		t.Skip("TEST_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedListing — вставить объявление в avito_listings (как делает
// parser-service до публикации parsed_ad).
func seedListing(t *testing.T, pool *pgxpool.Pool, id int64, dealType, description string, price int64) {
	t.Helper()
	ctx := context.Background()
	cleanup := func() {
		pool.Exec(ctx, `DELETE FROM ad_roi_results WHERE ad_id=$1`, id)
		pool.Exec(ctx, `DELETE FROM analysis_results WHERE ad_id=$1`, id)
		pool.Exec(ctx, `DELETE FROM avito_listings WHERE id=$1`, id)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := pool.Exec(ctx, `
		INSERT INTO avito_listings (id, url, url_path, category, deal_type, title, price,
		                            studio, total_area, address, description, raw, params)
		VALUES ($1, $2, $2, 'kvartiry', $3, 'Квартира-студия', $4, true, 24.0, $5, $6, '{}'::jsonb, '{}'::jsonb)`,
		id,
		fmt.Sprintf("https://www.avito.ru/spb/kvartiry/it6_%d", id),
		dealType, price,
		"Санкт-Петербург, Парашютная ул., 42к2",
		description); err != nil {
		t.Fatalf("seed %d: %v", id, err)
	}
}

// adsAvailable — отвечает ли ads-service gRPC (пустой UpdateAnalysis
// вернул бы InvalidArgument — транспорт жив).
func adsAvailable(t *testing.T, addr string) bool {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = adsv1.NewAdsServiceClient(conn).UpdateAnalysis(ctx, &adsv1.UpdateAnalysisRequest{})
	return err != nil && !strings.Contains(err.Error(), "context deadline") && !strings.Contains(err.Error(), "Unavailable")
}

// TestPipeline_ParsedAdToRoiAndAds — полный событийный прогон: parsed_ad
// → оценка → ad_roi_results (+ analysis_results через ads-service, если
// он поднят); parse_error → ack без расчёта.
func TestPipeline_ParsedAdToRoiAndAds(t *testing.T) {
	kcfg := requireKafka(t)
	pool := requireDSN(t)
	adsAddr := os.Getenv("ADS_GRPC_ADDR")
	if adsAddr == "" {
		adsAddr = "localhost:50052" // локальный прогон вне compose
	}

	seedListing(t, pool, itSaleID, "sale", "Студия без мебели.", 7_600_000)
	seedListing(t, pool, itRent1, "rent_long", "Студия без мебели, сдаём надолго.", 30_000)
	seedListing(t, pool, itRent2, "rent_long", "Студия с мебелью и техникой.", 34_000)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ev := evaluate.NewEvaluator(source.NewDBSource(pool), evaluate.DefaultConfig())
	// Ретраи downstream ограничены, чтобы тест не висел на недоступном
	// ads-service (поведение «не валим поток» проверяется unit-тестами).
	notifier := NewAdsNotifier(adsAddr, quiet(),
		WithNotifyAttempts(3), WithNotifyBackoff(func(int) time.Duration { return 300 * time.Millisecond }))
	handler := NewHandler(ev, &backfill.Runner{Pool: pool, Config: evaluate.DefaultConfig()}, notifier,
		evaluate.DefaultConfig(), quiet())

	group := fmt.Sprintf("analyzer-it-%d", time.Now().UnixNano())
	cons := pkgkafka.NewConsumer(kcfg, pkgkafka.TopicParsedAds, group, handler.HandleMessage,
		pkgkafka.WithLogger(quiet()))
	go func() { _ = cons.Run(ctx) }()

	// Тестовое parsed_ad (как их публикует parser-service).
	prod := pkgkafka.NewProducer(kcfg, pkgkafka.WithProducerLogger(quiet()))
	defer prod.Close()
	adID := int64(itSaleID)
	if err := prod.PublishParsedAd(ctx, pkgkafka.ParsedAd{
		RequestID: fmt.Sprintf("it-6-%d", time.Now().UnixNano()),
		Ad:        adsAdFromID(adID),
	}); err != nil {
		t.Fatalf("publish parsed_ad: %v", err)
	}

	// ad_roi_results должна появиться (at-least-once, полл до 30с).
	waitFor(t, 30*time.Second, "строка ad_roi_results", func() bool {
		var status string
		err := pool.QueryRow(context.Background(),
			`SELECT status FROM ad_roi_results WHERE ad_id=$1`, adID).Scan(&status)
		return err == nil && status == "ok"
	})
	var yield, rentMedian *float64
	var comps, clusterN *int
	if err := pool.QueryRow(context.Background(), `
		SELECT yield_unfurnished_pct, rent_median_unfurnished, comps_unfurnished, cluster_n
		FROM ad_roi_results WHERE ad_id=$1`, adID).
		Scan(&yield, &rentMedian, &comps, &clusterN); err != nil {
		t.Fatalf("ad_roi_results: %v", err)
	}
	t.Logf("ad_roi_results[%d]: yield_unfurnished=%v rent_median_unfurnished=%v comps=%v cluster_n=%v",
		adID, yield, rentMedian, comps, clusterN)

	// analysis_results (ads-service) — только если ads поднят.
	if adsAvailable(t, adsAddr) {
		waitFor(t, 30*time.Second, "строка analysis_results (ads-service)", func() bool {
			var n int
			err := pool.QueryRow(context.Background(),
				`SELECT count(*) FROM analysis_results WHERE ad_id=$1 AND status='ok'`, adID).Scan(&n)
			return err == nil && n == 1
		})
		t.Logf("analysis_results[%d] записан через AdsService.UpdateAnalysis", adID)
	} else {
		t.Logf("ads-service (%s) недоступен — assertion analysis_results пропущен", adsAddr)
	}

	// parse_error: лог + ack, расчёт не запускается (счётчик растёт).
	before := handler.ParseErrorsTotal()
	if err := prod.PublishParseError(ctx, pkgkafka.ParseError{
		RequestID: fmt.Sprintf("it-6-err-%d", time.Now().UnixNano()),
		Error:     "итоговая ошибка парсинга (тест #6)",
	}); err != nil {
		t.Fatalf("publish parse_error: %v", err)
	}
	waitFor(t, 30*time.Second, "parse_error обработан и ack-нут", func() bool {
		return handler.ParseErrorsTotal() > before
	})

	cancel() // graceful shutdown консьюмера
}

func adsAdFromID(id int64) *adsv1.Ad {
	return &adsv1.Ad{
		Id:       id,
		Url:      fmt.Sprintf("https://www.avito.ru/spb/kvartiry/it6_%d", id),
		UrlPath:  fmt.Sprintf("/spb/kvartiry/it6_%d", id),
		Title:    "Квартира-студия",
		DealType: "sale",
		Category: "kvartiry",
		Price:    7_600_000,
		Studio:   true,
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s (за %v)", what, timeout)
}
