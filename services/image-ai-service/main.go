// image-ai-service — детекция планировок среди фото объявления и
// связка объявления с планировкой (issue #58).
//
// Режимы:
//   - HTTP API (по умолчанию): /health, /api/detect, /api/ads/check, /api/floor-plan
//   - -check-ad <id>           — разовая проверка фото объявления и запись вердиктов
//   - -plan <id>               — вывести основную планировку объявления (или null)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/yourusername/real-estate-analyzer/image-ai-service/internal/floorplan"
	"github.com/yourusername/real-estate-analyzer/image-ai-service/internal/server"
	"github.com/yourusername/real-estate-analyzer/image-ai-service/internal/storage"
)

func main() {
	var (
		checkAd = flag.Int64("check-ad", 0, "ad_id — разовая проверка фото объявления (детекция + запись в ad_floor_plans) и выход")
		plan    = flag.Int64("plan", 0, "ad_id — вывести основную планировку объявления и выход")
		photos  = flag.String("photos", "", "JSON-массив ссылок на фото для -check-ad (по умолчанию — images из avito_listings)")
	)
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := storage.NewStorage(ctx, dsn())
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer store.Close()

	srv := server.New(floorplan.NewHeuristicDetector(), store)

	switch {
	case *checkAd != 0:
		res, err := srv.CheckAd(ctx, *checkAd, parsePhotos(*photos))
		if err != nil {
			log.Fatalf("check ad %d: %v", *checkAd, err)
		}
		printJSON(res)
		return
	case *plan != 0:
		rec, err := store.MainPlan(ctx, *plan)
		if err != nil {
			log.Fatalf("main plan %d: %v", *plan, err)
		}
		if rec == nil {
			fmt.Println("null")
			return
		}
		printJSON(rec)
		return
	}

	mux := http.NewServeMux()
	srv.Register(mux)
	addr := ":" + env("HTTP_PORT", "8080")
	log.Printf("image-ai-service listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// dsn собирается из переменных окружения так же, как в parser-service.
func dsn() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=%s",
		env("DB_USER", "analyzer"), env("DB_PASSWORD", "secret"),
		env("DB_HOST", "postgres"), env("DB_PORT", "5432"),
		env("DB_NAME", "analyzer"), env("DB_SSLMODE", "disable"),
	)
}

func parsePhotos(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		log.Fatalf("-photos: %v", err)
	}
	return out
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
