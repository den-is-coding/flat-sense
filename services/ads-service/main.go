// ads-service (issue #5): консьюмер parsed-ads (group ads-service)
// хранит проекцию объявлений в PostgreSQL (таблица ads), gRPC
// AdsService отдаёт объявления и результаты анализа (контракт #2).
//
// Окружение: DATABASE_URL (или DB_*), GRPC_PORT (50052),
// HTTP_PORT (8080, /health), KAFKA_BROKERS (пусто — консьюмер не
// стартует, работает только gRPC), KAFKA_GROUP_ID (ads-service).
//
// БД может быть недоступна на старте: сервис всё равно поднимает
// gRPC/HTTP, а консьюмер уложит необработанные сообщения в DLQ после
// ретраев — паники и рестарты не нужны.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/yourusername/real-estate-analyzer/ads-service/internal/adapter/consumer"
	"github.com/yourusername/real-estate-analyzer/ads-service/internal/adapter/grpcapi"
	"github.com/yourusername/real-estate-analyzer/ads-service/internal/adapter/postgres"
	"github.com/yourusername/real-estate-analyzer/ads-service/internal/config"
	"github.com/yourusername/real-estate-analyzer/pkg/kafka"
	adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"
)

func main() {
	if err := run(); err != nil {
		slog.Error("ads-service: фатальная ошибка", "err", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := config.FromEnv()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Пул ленивый: при недоступной БД сервис стартует и отвечает
	// ошибками (Unavailable), не паникуя.
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("postgres pool: %w", err)
	}
	defer pool.Close()
	store := postgres.NewStore(pool)

	if err := store.Ping(ctx); err != nil {
		log.Warn("ads-service: БД недоступна на старте — продолжаю, gRPC/консьюмер вернут ошибки",
			"err", err)
	} else {
		log.Info("ads-service: БД доступна")
	}

	// Консьюмер parsed-ads (KAFKA_BROKERS пуст → не стартует).
	consumerDone := startConsumer(ctx, cfg, store, log)

	// gRPC AdsService. Порт занимаем сразу: нечитаемый порт — фатальная
	// ошибка старта, в отличие от недоступной БД.
	lis, err := net.Listen("tcp", ":"+cfg.GRPCPort)
	if err != nil {
		return fmt.Errorf("grpc listen :%s: %w", cfg.GRPCPort, err)
	}
	grpcServer := grpc.NewServer()
	adsv1.RegisterAdsServiceServer(grpcServer, grpcapi.NewServer(store, log))
	grpcStopped := make(chan struct{})
	go func() {
		defer close(grpcStopped)
		log.Info("ads-service: gRPC слушает", "port", cfg.GRPCPort)
		if err := grpcServer.Serve(lis); err != nil {
			log.Error("ads-service: gRPC server остановлен с ошибкой", "err", err)
		}
	}()

	// Health-эндпоинт для docker/monitoring (внутренний, не публикуется наружу).
	httpSrv := serveHealth(cfg.HTTPPort, log)

	// Graceful shutdown по SIGTERM/SIGINT: сначала приём новых gRPC
	// вызовов (GracefulStop), затем консьюмер (дождаться коммита
	// текущего сообщения), затем HTTP и пул.
	<-ctx.Done()
	log.Info("ads-service: сигнал остановки, graceful shutdown")

	done := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Warn("ads-service: GracefulStop не уложился в 10с, принудительная остановка")
		grpcServer.Stop()
	}

	if consumerDone != nil {
		select {
		case <-consumerDone:
		case <-time.After(15 * time.Second):
			log.Warn("ads-service: консьюмер не остановился за 15с")
		}
	}
	<-grpcStopped
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("ads-service: http shutdown", "err", err)
	}
	log.Info("ads-service: остановлен")
	return nil
}

// startConsumer — консьюмер топика parsed-ads; nil, если KAFKA_BROKERS
// не задан (тогда сервис работает только как gRPC-хранилище).
func startConsumer(ctx context.Context, cfg config.Config, store *postgres.Store, log *slog.Logger) <-chan struct{} {
	if len(cfg.KafkaBrokers) == 0 {
		log.Info("ads-service: KAFKA_BROKERS не задан — консьюмер parsed-ads не запущен")
		return nil
	}
	kcfg := kafka.Config{Brokers: cfg.KafkaBrokers}
	handler := consumer.NewHandler(store, log)
	consumer := kafka.NewConsumer(kcfg, kafka.TopicParsedAds, cfg.KafkaGroupID,
		handler.HandleMessage, kafka.WithLogger(log))
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Run возвращает nil по отмене ctx (SIGTERM) — graceful shutdown.
		if err := consumer.Run(ctx); err != nil {
			log.Error("ads-service: kafka consumer остановлен с ошибкой", "err", err)
		}
	}()
	log.Info("ads-service: kafka consumer запущен",
		"topic", kafka.TopicParsedAds, "group", cfg.KafkaGroupID,
		"brokers", cfg.KafkaBrokers)
	return done
}

// serveHealth — мини-HTTP с GET /health (liveness); останавливается
// через Shutdown после gRPC/консьюмера.
func serveHealth(port string, log *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	srv := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		log.Info("ads-service: http health слушает", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("ads-service: http server остановлен с ошибкой", "err", err)
		}
	}()
	return srv
}
