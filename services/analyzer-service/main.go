// analyzer-service — расчёт оценки объявления по окупаемости
// с учётом меблировки (issue #64, методика кластеризации #55).
//
// Режимы:
//   - HTTP API (по умолчанию): POST /evaluate {url|adId} — источник БД avito_listings
//   - событийный поток (#6): KAFKA_BROKERS задан → консьюмер parsed-ads
//     (group analyzer-service) считает оценку по parsed_ad, пишет
//     ad_roi_results и уведомляет ads-service (ADS_GRPC_ADDR) через
//     AdsService.UpdateAnalysis; KAFKA_BROKERS пуст → консьюмер не
//     стартует (HTTP/gRPC-режим как раньше)
//   - gRPC AnalyzerService (#6): EvaluateAd / UpdateAnalysis на GRPC_PORT
//     (50056; контракт pkg/proto/analyzer/v1)
//   - -evaluate <id|url> [-dump-dir a,b] — разовая оценка по дампам парсера
//     (data/<кампания>/listings), выход — JSON отчёта в stdout
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	pkgkafka "github.com/yourusername/real-estate-analyzer/pkg/kafka"
	analyzerv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/analyzer/v1"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/backfill"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/grpcapi"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/labeler"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/photoai"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/pipeline"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/source"
)

func main() {
	// JSON-логи (критерий #6) — slog по умолчанию для всего сервиса.
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	var (
		evalTarget  = flag.String("evaluate", "", "id или URL объявления — разовая оценка и выход (источник: -dump-dir, иначе БД)")
		dumpDirs    = flag.String("dump-dir", "", "каталоги дампов парсера через запятую (для -evaluate и HTTP-режима без БД)")
		labelFurn   = flag.Bool("label-furnishing", false, "разметить меблировку всех объявлений БД в ad_furnishing и выйти")
		labelPhotos = flag.Bool("label-furnishing-photos", false, "фото-фолбэк (#110): разметить меблировку по фото для объявлений с неоднозначным текстом (нужен IMAGEAI_URL) и выйти")
		backfillROI = flag.Bool("backfill-roi", false, "пересчитать окупаемость всех объявлений-продаж в ad_roi_results и выйти (идемпотентно, источник — БД)")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := configFromEnv()

	// Режим backfill окупаемости: БД → оценка всех продаж → ad_roi_results.
	if *backfillROI {
		pool := mustPool(ctx)
		defer pool.Close()
		run := &backfill.Runner{
			Source: source.NewDBSource(pool),
			Pool:   pool,
			Config: cfg,
		}
		rep, err := run.Run(ctx)
		if err != nil {
			log.Error("backfill", "err", err)
			os.Exit(1)
		}
		printJSON(rep)
		return
	}

	// Режим разметки мебели: БД → эвристика → ad_furnishing.
	if *labelFurn {
		pool := mustPool(ctx)
		defer pool.Close()
		sum, byType, err := labeler.LabelAll(ctx, pool)
		if err != nil {
			log.Error("label", "err", err)
			os.Exit(1)
		}
		printJSON(map[string]any{"summary": sum, "byDealType": byType})
		return
	}

	// Режим фото-фолбэка (issue #110): неоднозначный текст → фото → кэш.
	if *labelPhotos {
		pool := mustPool(ctx)
		defer pool.Close()
		st, err := labeler.LabelPhotos(ctx, pool, photoai.New(env("IMAGEAI_URL", "http://localhost:8089")))
		if err != nil {
			log.Error("label photos", "err", err)
			os.Exit(1)
		}
		printJSON(st)
		return
	}

	// CLI-режим: оценка по дампам из директории либо по БД.
	if *evalTarget != "" {
		runCLI(ctx, cfg, *evalTarget, *dumpDirs)
		return
	}

	if err := serve(ctx, cfg, *dumpDirs, log); err != nil {
		log.Error("analyzer-service: фатальная ошибка", "err", err)
		os.Exit(1)
	}
}

// serve — основной (серверный) режим: HTTP API + gRPC AnalyzerService +
// опциональный консьюмер parsed-ads (KAFKA_BROKERS), graceful shutdown
// по SIGTERM/SIGINT.
func serve(ctx context.Context, cfg evaluate.Config, dumpDirs string, log *slog.Logger) error {
	// HTTP-режим: источник — БД, либо дампы (если задан -dump-dir,
	// удобно для локальной проверки без арендного пула в БД).
	var (
		src  evaluate.Source
		pool *pgxpool.Pool
	)
	if dumpDirs != "" {
		ds, err := source.NewDumpSource(strings.Split(dumpDirs, ",")...)
		if err != nil {
			return fmt.Errorf("dump source: %w", err)
		}
		src = ds
	} else {
		pool = mustPool(ctx)
		defer pool.Close()
		src = source.NewDBSource(pool)
	}

	ev := evaluate.NewEvaluator(src, cfg)
	// Фото-фолбэк (issue #110): недоступность image-ai деградирует
	// в текстовый режим внутри Evaluator.
	ev.Photo = photoai.New(env("IMAGEAI_URL", "http://image-ai-service:8080"))

	// Консьюмер parsed-ads (#6). Без БД (режим -dump-dir) потоковый
	// режим смысла не имеет — данные читаются из avito_listings.
	consumerDone := startConsumer(ctx, cfg, pool, ev, log)

	// gRPC AnalyzerService (порт GRPC_PORT, 50056). Сейвер — тот же
	// Runner backfill: поток и gRPC пишут ad_roi_results одинаково.
	// В режиме -dump-dir БД нет — сейвер nil (UpdateAnalysis ответит
	// Unavailable, EvaluateAd работает по дампам).
	var saver grpcapi.RowSaver
	if pool != nil {
		saver = &backfill.Runner{Pool: pool, Config: cfg}
	}
	lis, err := net.Listen("tcp", ":"+env("GRPC_PORT", "50056"))
	if err != nil {
		return fmt.Errorf("grpc listen :%s: %w", env("GRPC_PORT", "50056"), err)
	}
	grpcServer := grpc.NewServer()
	analyzerv1.RegisterAnalyzerServiceServer(grpcServer, grpcapi.NewServer(ev, saver, cfg, log))
	grpcStopped := make(chan struct{})
	go func() {
		defer close(grpcStopped)
		log.Info("analyzer-service: gRPC слушает", "port", env("GRPC_PORT", "50056"))
		if err := grpcServer.Serve(lis); err != nil {
			log.Error("analyzer-service: gRPC server остановлен с ошибкой", "err", err)
		}
	}()

	// HTTP API — как раньше.
	srv := newServer(ev)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("POST /evaluate", srv.handleEvaluate)
	mux.HandleFunc("GET /{$}", srv.handleForm)
	mux.HandleFunc("GET /evaluate-page", srv.handleEvaluatePage)
	httpSrv := &http.Server{Addr: ":" + env("HTTP_PORT", "8080"), Handler: mux}
	httpStopped := make(chan struct{})
	go func() {
		defer close(httpStopped)
		log.Info("analyzer-service listening on :"+env("HTTP_PORT", "8080"), "port", env("HTTP_PORT", "8080"))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("analyzer-service: http server остановлен с ошибкой", "err", err)
		}
	}()

	// Graceful shutdown по SIGTERM/SIGINT: сначала HTTP и gRPC (перестать
	// принимать новые вызовы), затем консьюмер (дождаться коммита текущего
	// сообщения), затем пул (defer).
	<-ctx.Done()
	log.Info("analyzer-service: сигнал остановки, graceful shutdown")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("analyzer-service: http shutdown", "err", err)
	}
	<-httpStopped

	done := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Warn("analyzer-service: GracefulStop не уложился в 10с, принудительная остановка")
		grpcServer.Stop()
	}
	<-grpcStopped

	if consumerDone != nil {
		select {
		case <-consumerDone:
		case <-time.After(15 * time.Second):
			log.Warn("analyzer-service: консьюмер не остановился за 15с")
		}
	}
	log.Info("analyzer-service: остановлен")
	return nil
}

// startConsumer — консьюмер топика parsed-ads (group analyzer-service).
// KAFKA_BROKERS пуст → nil (консьюмер не стартует, HTTP/gRPC-режим).
// В режиме -dump-dir (pool == nil) консьюмеру не из чего считать — warn.
func startConsumer(ctx context.Context, cfg evaluate.Config, pool *pgxpool.Pool, ev *evaluate.Evaluator, log *slog.Logger) <-chan struct{} {
	brokers := splitList(os.Getenv("KAFKA_BROKERS"))
	if len(brokers) == 0 {
		log.Info("analyzer-service: KAFKA_BROKERS не задан — консьюмер parsed-ads не запущен (HTTP/gRPC-режим)")
		return nil
	}
	if pool == nil {
		log.Warn("analyzer-service: KAFKA_BROKERS задан, но источник — дампы (-dump-dir): консьюмер требует БД (avito_listings/ad_roi_results) и не запущен")
		return nil
	}
	group := env("KAFKA_GROUP_ID", "analyzer-service")

	// Downstream-уведомление ads-service (issue #6): ADS_GRPC_ADDR,
	// в docker-compose — ads-service:50052. Число попыток ограничено
	// (ADS_NOTIFY_ATTEMPTS): после N неудач — лог-ошибка и ack, поток
	// не валим (результат уже в ad_roi_results).
	notifyOpts := []pipeline.AdsNotifierOption{}
	if v := os.Getenv("ADS_NOTIFY_ATTEMPTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			notifyOpts = append(notifyOpts, pipeline.WithNotifyAttempts(n))
		}
	}
	adsAddr := env("ADS_GRPC_ADDR", "ads-service:50052")
	notifier := pipeline.NewAdsNotifier(adsAddr, log, notifyOpts...)
	handler := pipeline.NewHandler(ev, &backfill.Runner{Pool: pool, Config: cfg}, notifier, cfg, log)
	cons := pkgkafka.NewConsumer(pkgkafka.Config{Brokers: brokers}, pkgkafka.TopicParsedAds, group,
		handler.HandleMessage, pkgkafka.WithLogger(log))

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer notifier.Close()
		// Run возвращает nil по отмене ctx (SIGTERM) — graceful shutdown.
		if err := cons.Run(ctx); err != nil {
			log.Error("analyzer-service: kafka consumer остановлен с ошибкой", "err", err)
		}
	}()
	log.Info("analyzer-service: kafka consumer запущен",
		"topic", pkgkafka.TopicParsedAds, "group", group, "brokers", brokers,
		"ads_grpc_addr", adsAddr)
	return done
}

// mustPool — подключение к БД или фатальная ошибка (серверные/CLI-режимы
// без БД бессмысленны).
func mustPool(ctx context.Context) *pgxpool.Pool {
	pool, err := pgxpool.New(ctx, dsn())
	if err != nil {
		slog.Error("db", "err", err)
		os.Exit(1)
	}
	if err := pool.Ping(ctx); err != nil {
		slog.Error("db ping", "err", err)
		os.Exit(1)
	}
	return pool
}

func runCLI(ctx context.Context, cfg evaluate.Config, target, dumpDirs string) {
	id := source.ExtractListingID(target)
	if id == 0 {
		slog.Error("не удалось извлечь id объявления", "target", target)
		os.Exit(1)
	}
	var ev *evaluate.Evaluator
	if dumpDirs != "" {
		src, err := source.NewDumpSource(strings.Split(dumpDirs, ",")...)
		if err != nil {
			slog.Error("dump source", "err", err)
			os.Exit(1)
		}
		ev = evaluate.NewEvaluator(src, cfg)
	} else {
		// Источник — БД (DB_HOST=localhost при запуске на хосте).
		pool, err := pgxpool.New(ctx, dsn())
		if err != nil {
			slog.Error("db", "err", err)
			os.Exit(1)
		}
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			slog.Error("db ping", "err", err)
			os.Exit(1)
		}
		ev = evaluate.NewEvaluator(source.NewDBSource(pool), cfg)
	}
	rep, err := ev.EvaluateByID(ctx, id)
	if err != nil {
		slog.Error("evaluate", "err", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(rep)
}

// apiServer — HTTP-обвязка Evaluator.
type apiServer struct{ ev *evaluate.Evaluator }

func newServer(ev *evaluate.Evaluator) *apiServer { return &apiServer{ev: ev} }

// evaluateRequest — тело POST /evaluate.
type evaluateRequest struct {
	URL  string `json:"url,omitempty"`
	AdID int64  `json:"adId,omitempty"`
}

func (s *apiServer) handleEvaluate(w http.ResponseWriter, r *http.Request) {
	var req evaluateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json: " + err.Error()})
		return
	}
	id := req.AdID
	if id == 0 && req.URL != "" {
		id = source.ExtractListingID(req.URL)
	}
	if id == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url or adId is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rep, err := s.ev.EvaluateByID(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	// «Нет арендных данных» — валидный исход: 200 с вежливым отказом.
	writeJSON(w, http.StatusOK, rep)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// printJSON — вывод результата CLI-режимов в stdout.
func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// configFromEnv — константы методики из окружения (см. критерии #64).
func configFromEnv() evaluate.Config {
	cfg := evaluate.DefaultConfig()
	if v := os.Getenv("FURNISHING_COST_RUB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			cfg.FurnishingCostRUB = n
		}
	}
	if v := os.Getenv("MIN_CLUSTER_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MinClusterSize = n
		}
	}
	if v := os.Getenv("REALTOR_FEE_PCT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			cfg.RealtorFeePct = f
		}
	}
	if v := os.Getenv("TITLE_INSURANCE_PCT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			cfg.TitleInsurancePct = f
		}
	}
	if v := os.Getenv("DEAL_FIXED_COSTS_RUB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			cfg.DealFixedCostsRUB = n
		}
	}
	return cfg
}

func dsn() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=%s",
		env("DB_USER", "analyzer"), env("DB_PASSWORD", "secret"),
		env("DB_HOST", "postgres"), env("DB_PORT", "5432"),
		env("DB_NAME", "analyzer"), env("DB_SSLMODE", "disable"),
	)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// splitList — список через запятую; пустая строка → nil (в отличие от
// pkg/kafka.ConfigFromEnv, который подставляет localhost:9092: флаг
// #6 — «KAFKA_BROKERS пуст → консьюмер не стартует»).
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
