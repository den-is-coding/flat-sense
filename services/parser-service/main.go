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

	"github.com/yourusername/real-estate-analyzer/parser-service/internal/admin"
	"github.com/yourusername/real-estate-analyzer/parser-service/internal/avito"
)

func main() {
	// AVITO_BASE_URL: переопределение базового URL Авито (мок-сервер для тестов).
	avito.SetBaseURL(os.Getenv("AVITO_BASE_URL"))

	var (
		onceTask   = flag.String("task", "", "JSON фильтров — разовый прогон парсинга и выход")
		onceURL    = flag.String("url", "", "URL выдачи Авито (скопированный из браузера) — разовый прогон и выход")
		onceItem   = flag.String("item", "", "URL карточки объявления — разовый разбор и выход")
		reparse    = flag.String("reparse", "", "source_task — пере-парсинг raw из БД актуальным парсером (офлайн)")
		exportOnly = flag.String("export", "", "source_task — выгрузка из БД в -out-dir без нового сбора")
		maxPages   = flag.Int("max-pages", 0, "максимум страниц выдачи (0 — из окружения/по умолчанию)")
		noDetails  = flag.Bool("no-details", false, "не запрашивать карточку каждого объявления")
		sourceTask = flag.String("source-task", "", "метка прогона (0 — из окружения PARSE_SOURCE_TASK)")
		outDir     = flag.String("out-dir", "", "директория выгрузки карточек (JSON) и изображений после прогона")
		onlyStudio = flag.Bool("only-studio", false, "выгрузка: только студии")
		minYear    = flag.Int("min-year", 0, "выгрузка: год постройки/сдачи >= значения (0 = без фильтра)")
	)
	flag.Parse()

	cfg := loadConfig()
	if *maxPages > 0 {
		cfg.MaxPages = *maxPages
	}
	if *noDetails {
		cfg.FetchDetails = false
	}
	if *onlyStudio {
		cfg.OnlyStudios = true // фильтр действует и на поиск, и на экспорт
	}
	if *sourceTask != "" {
		cfg.SourceTask = *sourceTask
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	storage, err := avito.NewStorage(ctx, cfg.DSN)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer storage.Close()

	client, err := avito.NewClient(cfg.Avito)
	if err != nil {
		log.Fatalf("avito client: %v", err)
	}
	service := avito.NewService(client, storage, cfg.serviceConfig())

	// CLI-режимы: разовый прогон и выход.
	switch {
	case *exportOnly != "":
		if *outDir == "" {
			log.Fatalf("-export требует -out-dir")
		}
		exportStats, err := avito.ExportListings(ctx, storage, client, *exportOnly, avito.ExportOptions{
			OutDir: *outDir, OnlyStudio: *onlyStudio, MinYear: *minYear,
		})
		if err != nil {
			log.Fatalf("export: %v", err)
		}
		printJSON(exportStats)
		return
	case *reparse != "":
		rows, err := storage.SelectRawByTask(ctx, *reparse)
		if err != nil {
			log.Fatalf("select raw: %v", err)
		}
		updated := 0
		for _, r := range rows {
			l, err := avito.ReparseSearchItem(r.Raw, r.Category, avito.DealKind(r.DealType), *reparse)
			if err != nil {
				log.Printf("reparse %d: %v", r.ID, err)
				continue
			}
			if _, err := storage.UpsertListing(ctx, l); err != nil {
				log.Printf("upsert %d: %v", r.ID, err)
				continue
			}
			updated++
		}
		printJSON(map[string]any{"sourceTask": *reparse, "rows": len(rows), "updated": updated})
		return
	case *onceItem != "":
		l, err := service.ParseOne(ctx, *onceItem)
		if err != nil {
			log.Fatalf("parse item: %v", err)
		}
		printJSON(l)
		return
	case *onceURL != "":
		f, err := avito.FiltersFromURL(*onceURL)
		if err != nil {
			log.Fatalf("filters from url: %v", err)
		}
		runParseAndExport(ctx, service, client, storage, f, cfg, *outDir, *onlyStudio, *minYear)
		return
	case *onceTask != "":
		f, err := avito.ParseFiltersJSON([]byte(*onceTask))
		if err != nil {
			log.Fatalf("filters: %v", err)
		}
		runParseAndExport(ctx, service, client, storage, f, cfg, *outDir, *onlyStudio, *minYear)
		return
	}

	// HTTP-режим.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("POST /api/parse", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Filters      *json.RawMessage `json:"filters"`
			URL          string           `json:"url"`
			MaxPages     int              `json:"maxPages"`
			FetchDetails *bool            `json:"fetchDetails"`
			SourceTask   string           `json:"sourceTask"`
			WithListings bool             `json:"withListings"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "bad json: %v", err)
			return
		}
		var (
			f   *avito.SearchFilters
			err error
		)
		switch {
		case req.Filters != nil:
			f, err = avito.ParseFiltersJSON([]byte(*req.Filters))
		case req.URL != "":
			f, err = avito.FiltersFromURL(req.URL)
		default:
			httpError(w, http.StatusBadRequest, "either 'filters' or 'url' is required")
			return
		}
		if err != nil {
			httpError(w, http.StatusBadRequest, "%v", err)
			return
		}

		rcfg := cfg
		if req.MaxPages > 0 {
			rcfg.MaxPages = req.MaxPages
		}
		if req.FetchDetails != nil {
			rcfg.FetchDetails = *req.FetchDetails
		}
		if req.SourceTask != "" {
			rcfg.SourceTask = req.SourceTask
		}
		svc := avito.NewService(client, storage, rcfg.serviceConfig())

		stats, err := svc.RunParse(r.Context(), f, req.WithListings)
		if err != nil {
			// частичный прогон возвращаем с кодом 207, полный провал — 502
			if stats != nil && stats.PagesFetched > 0 {
				writeJSON(w, http.StatusMultiStatus, stats)
				return
			}
			httpError(w, http.StatusBadGateway, "parse: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, stats)
	})
	mux.HandleFunc("GET /api/item", func(w http.ResponseWriter, r *http.Request) {
		u := r.URL.Query().Get("url")
		if u == "" {
			httpError(w, http.StatusBadRequest, "query param 'url' is required")
			return
		}
		l, err := service.ParseOne(r.Context(), u)
		if err != nil {
			httpError(w, http.StatusBadGateway, "parse item: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, l)
	})
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		runs, err := storage.ListRuns(r.Context(), limit)
		if err != nil {
			httpError(w, http.StatusInternalServerError, "list runs: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, runs)
	})

	// Админка (issue #57): включается при заданных ADMIN_LOGIN/ADMIN_PASSWORD.
	// TODO(#57): при появлении auth-service (Этап 2) перевести проверку
	// доступа на него; сейчас — креды из env deploy/.env.
	if login, pass := os.Getenv("ADMIN_LOGIN"), os.Getenv("ADMIN_PASSWORD"); login != "" && pass != "" {
		ttl := time.Duration(envInt("ADMIN_SESSION_TTL_HOURS", 12)) * time.Hour
		secret := os.Getenv("ADMIN_SESSION_SECRET")
		if secret == "" {
			secret = pass + "|flat-sense-admin"
		}
		admin.Register(mux, admin.NewStore(storage.Pool()), admin.NewSessions(secret, ttl), login, pass)
		log.Printf("admin routes enabled (user=%s)", login)
	} else {
		log.Printf("admin routes disabled: ADMIN_LOGIN/ADMIN_PASSWORD not set")
	}

	addr := ":" + cfg.HTTPPort
	log.Printf("parser-service listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// runParseAndPrint — устаревшее имя оставлено для совместимости вызовов из тестов.
func runParseAndPrint(ctx context.Context, service *avito.Service, f *avito.SearchFilters) {
	runParseAndExport(ctx, service, nil, nil, f, Config{}, "", false, 0)
}

// runParseAndExport выполняет прогон парсинга и, если задана outDir,
// выгружает объявления прогона (с фильтрами) и их изображения в директорию.
func runParseAndExport(ctx context.Context, service *avito.Service, client *avito.AvitoClient,
	storage *avito.Storage, f *avito.SearchFilters, cfg Config, outDir string, onlyStudio bool, minYear int) {
	log.Printf("run: %s", f)
	stats, err := service.RunParse(ctx, f, false)
	if err != nil {
		log.Printf("run finished with error: %v", err)
	}
	printJSON(stats)

	if outDir == "" {
		return
	}
	sourceTask := cfg.SourceTask
	if sourceTask == "" {
		sourceTask = fmt.Sprintf("cli-%d", time.Now().Unix())
	}
	log.Printf("export: dir=%s source_task=%s onlyStudio=%t minYear=%d", outDir, sourceTask, onlyStudio, minYear)
	exportStats, err := avito.ExportListings(ctx, storage, client, sourceTask, avito.ExportOptions{
		OutDir: outDir, OnlyStudio: onlyStudio, MinYear: minYear,
	})
	if err != nil {
		log.Printf("export: %v", err)
		return
	}
	printJSON(exportStats)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func httpError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]string{"error": fmt.Sprintf(format, args...)})
}

// Config — конфигурация сервиса из окружения.
type Config struct {
	DSN          string
	HTTPPort     string
	Avito        avito.ClientConfig
	MaxPages     int
	FetchDetails bool
	OnlyStudios  bool
	SourceTask   string
}

func (c Config) serviceConfig() avito.ServiceConfig {
	return avito.ServiceConfig{
		MaxPages:     c.MaxPages,
		FetchDetails: c.FetchDetails,
		OnlyStudios:  c.OnlyStudios,
		SourceTask:   c.SourceTask,
	}
}

func loadConfig() Config {
	return Config{
		DSN: fmt.Sprintf(
			"postgres://%s:%s@%s:%s/%s?sslmode=%s",
			env("DB_USER", "analyzer"), env("DB_PASSWORD", "secret"),
			env("DB_HOST", "postgres"), env("DB_PORT", "5432"),
			env("DB_NAME", "analyzer"), env("DB_SSLMODE", "disable"),
		),
		HTTPPort: env("HTTP_PORT", "8080"),
		Avito: avito.ClientConfig{
			Proxies:            splitList(env("AVITO_PROXIES", "")),
			ProxyRotationURL:   env("AVITO_PROXY_ROTATION_URL", ""),
			CookieString:       env("AVITO_COOKIES", ""),
			MinDelay:           time.Duration(envInt("AVITO_MIN_DELAY_MS", 2000)) * time.Millisecond,
			MaxDelay:           time.Duration(envInt("AVITO_MAX_DELAY_MS", 6000)) * time.Millisecond,
			LongPauseEvery:     envInt("AVITO_LONG_PAUSE_EVERY", 20),
			MaxRotations:       envInt("AVITO_MAX_ROTATIONS", 8),
			BlockThreshold:     envInt("AVITO_BLOCK_THRESHOLD", 3),
			RetryDelay:         time.Duration(envInt("AVITO_RETRY_DELAY_MS", 5000)) * time.Millisecond,
			BlockedCool:        time.Duration(envInt("AVITO_BLOCKED_COOL_SEC", 90)) * time.Second,
			TimeoutSeconds:     envInt("AVITO_TIMEOUT_SEC", 45),
			InsecureSkipVerify: env("AVITO_TLS_INSECURE", "") == "1",
		},
		MaxPages:     envInt("PARSE_MAX_PAGES", 0),
		FetchDetails: env("PARSE_FETCH_DETAILS", "1") == "1",
		SourceTask:   env("PARSE_SOURCE_TASK", ""),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
