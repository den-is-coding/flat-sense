// analyzer-service — расчёт оценки объявления по окупаемости
// с учётом меблировки (issue #64, методика кластеризации #55).
//
// Режимы:
//   - HTTP API (по умолчанию): POST /evaluate {url|adId} — источник БД avito_listings
//   - -evaluate <id|url> [-dump-dir a,b] — разовая оценка по дампам парсера
//     (data/<кампания>/listings), выход — JSON отчёта в stdout
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/source"
)

func main() {
	var (
		evalTarget = flag.String("evaluate", "", "id или URL объявления — разовая оценка и выход")
		dumpDirs   = flag.String("dump-dir", "", "каталоги дампов парсера через запятую (для -evaluate без БД)")
	)
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := configFromEnv()

	// CLI-режим: оценка по дампам из директории.
	if *evalTarget != "" {
		runCLI(ctx, cfg, *evalTarget, *dumpDirs)
		return
	}

	// HTTP-режим: источник — БД, либо дампы (если задан -dump-dir,
	// удобно для локальной проверки без арендного пула в БД).
	var src evaluate.Source
	if *dumpDirs != "" {
		ds, err := source.NewDumpSource(strings.Split(*dumpDirs, ",")...)
		if err != nil {
			log.Fatalf("dump source: %v", err)
		}
		src = ds
	} else {
		pool, err := pgxpool.New(ctx, dsn())
		if err != nil {
			log.Fatalf("db: %v", err)
		}
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			log.Fatalf("db ping: %v", err)
		}
		src = source.NewDBSource(pool)
	}

	ev := evaluate.NewEvaluator(src, cfg)
	srv := newServer(ev)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("POST /evaluate", srv.handleEvaluate)
	mux.HandleFunc("GET /{$}", srv.handleForm)
	mux.HandleFunc("GET /evaluate-page", srv.handleEvaluatePage)

	addr := ":" + env("HTTP_PORT", "8080")
	log.Printf("analyzer-service listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func runCLI(ctx context.Context, cfg evaluate.Config, target, dumpDirs string) {
	if dumpDirs == "" {
		log.Fatalf("-evaluate требует -dump-dir (каталоги дампов listings) либо HTTP-режим с БД")
	}
	src, err := source.NewDumpSource(strings.Split(dumpDirs, ",")...)
	if err != nil {
		log.Fatalf("dump source: %v", err)
	}
	id := source.ExtractListingID(target)
	if id == 0 {
		log.Fatalf("не удалось извлечь id объявления из %q", target)
	}
	ev := evaluate.NewEvaluator(src, cfg)
	rep, err := ev.EvaluateByID(ctx, id)
	if err != nil {
		log.Fatalf("evaluate: %v", err)
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

// configFromEnv — константы методики из окружения (см. критерии #64).
func configFromEnv() evaluate.Config {
	cfg := evaluate.DefaultConfig()
	if v := os.Getenv("FURNISHING_COST_RUB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			cfg.FurnishingCostRUB = n
		}
	}
	if v := os.Getenv("AREA_TOLERANCE_PCT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			cfg.AreaTolerancePct = f
		}
	}
	if v := os.Getenv("MIN_CLUSTER_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MinClusterSize = n
		}
	}
	if v := os.Getenv("CLUSTER_RADIUS_M"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.ClusterRadiusM = n
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
