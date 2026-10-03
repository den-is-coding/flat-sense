package avito

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExportOptions — настройки выгрузки объявлений прогона в директорию.
type ExportOptions struct {
	OutDir     string // корневая директория выгрузки
	OnlyStudio bool   // только студии
	MinYear    int    // год постройки/сдачи >= значения (0 = без фильтра)

	ImageDelayMin time.Duration // пауза между скачиваниями изображений
	ImageDelayMax time.Duration
}

// ExportListings выгружает объявления с заданным source_task из БД в outDir:
//   - listings/<id>.json — все колонки карточки, включая исходный JSON (raw);
//   - images/<id>/<n>_<w>x<h>.<ext> — изображения (лучший доступный размер).
//
// Возвращает манифест прогона (также пишется в <outDir>/run.json).
func ExportListings(ctx context.Context, s *Storage, c *AvitoClient, sourceTask string, opts ExportOptions) (map[string]any, error) {
	if sourceTask == "" {
		return nil, fmt.Errorf("source task is empty")
	}
	if opts.ImageDelayMin <= 0 {
		opts.ImageDelayMin = 300 * time.Millisecond
	}
	if opts.ImageDelayMax < opts.ImageDelayMin {
		opts.ImageDelayMax = opts.ImageDelayMin + 500*time.Millisecond
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	listingsDir := filepath.Join(opts.OutDir, "listings")
	imagesDir := filepath.Join(opts.OutDir, "images")
	for _, d := range []string{opts.OutDir, listingsDir, imagesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	// Числовые колонки NUMERIC кастуются к float8, чтобы pgx вернул обычные числа.
	rows, err := s.pool.Query(ctx, `
SELECT id, url, url_path, category, deal_type, title, description,
       price, price_currency, price_per_unit, price_unit, price_meta,
       rooms, studio,
       total_area::float8 AS total_area, living_area::float8 AS living_area,
       kitchen_area::float8 AS kitchen_area, land_area::float8 AS land_area,
       floor, floors_total, house_type, renovation, balcony, bathroom, year_built,
       address, region, city, district, metro, lat, lng, geo,
       seller_name, seller_type, seller_url, seller_rating::float8 AS seller_rating, seller,
       images, image_count,
       views_count, contacts_count, favorites_count,
       published_at, refreshed_at,
       params, raw, source_task, first_seen_at, last_seen_at, updated_at
FROM avito_listings WHERE source_task=$1 ORDER BY id`, sourceTask)
	if err != nil {
		return nil, fmt.Errorf("query listings: %w", err)
	}
	defer rows.Close()

	stats := map[string]any{
		"sourceTask": sourceTask, "onlyStudio": opts.OnlyStudio, "minYear": opts.MinYear,
		"exported": 0, "skipped": 0, "yearUnknown": 0, "imagesDownloaded": 0, "imageErrors": 0,
		"generatedAt": time.Now().UTC().Format(time.RFC3339),
	}

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		vals, err := rows.Values()
		if err != nil {
			return stats, fmt.Errorf("scan row: %w", err)
		}
		m := map[string]any{}
		for i, fd := range rows.FieldDescriptions() {
			m[string(fd.Name)] = vals[i]
		}
		// jsonb-колонки pgx может вернуть как []byte или string: в JSON-выгрузке
		// отдаём их как сырой JSON (иначе encoding/json закодирует []byte в base64).
		for _, col := range []string{"price_meta", "geo", "seller", "params", "raw", "images"} {
			if v, ok := m[col]; ok {
				switch t := v.(type) {
				case []byte:
					m[col] = json.RawMessage(t)
				case string:
					m[col] = json.RawMessage(t)
				}
			}
		}

		if opts.OnlyStudio && m["studio"] != true {
			stats["skipped"] = stats["skipped"].(int) + 1
			continue
		}
		if opts.MinYear > 0 {
			yb, ok := asInt64(m["year_built"])
			if ok && yb < int64(opts.MinYear) {
				stats["skipped"] = stats["skipped"].(int) + 1
				continue
			}
			if !ok {
				// год неизвестен (карточка не добылась) — не выбрасываем,
				// но считаем отдельно
				stats["yearUnknown"] = stats["yearUnknown"].(int) + 1
			}
		}

		id := fmt.Sprintf("%v", m["id"])
		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return stats, fmt.Errorf("marshal listing %s: %w", id, err)
		}
		if err := os.WriteFile(filepath.Join(listingsDir, id+".json"), data, 0o644); err != nil {
			return stats, fmt.Errorf("write listing %s: %w", id, err)
		}
		stats["exported"] = stats["exported"].(int) + 1

		// Изображения карточки. pgx декодирует jsonb в нативные значения
		// ([]interface{}, map[string]any); []byte/string — на всякий случай.
		var raw []byte
		switch t := m["images"].(type) {
		case []byte:
			raw = t
		case string:
			raw = []byte(t)
		case nil:
			continue
		default:
			b, merr := json.Marshal(t)
			if merr != nil {
				log.Printf("[export] listing %s: images marshal: %v", id, merr)
				continue
			}
			raw = b
		}
		var imgs []Image
		if err := json.Unmarshal(raw, &imgs); err != nil || len(imgs) == 0 {
			log.Printf("[export] listing %s: images unmarshal: %v (len=%d)", id, err, len(imgs))
			continue
		}
		listingImgDir := filepath.Join(imagesDir, id)
		if err := os.MkdirAll(listingImgDir, 0o755); err != nil {
			return stats, err
		}
		for n, img := range imgs {
			if img.URL == "" {
				continue
			}
			body, err := c.FetchAsset(img.URL)
			if err != nil {
				stats["imageErrors"] = stats["imageErrors"].(int) + 1
				continue
			}
			name := fmt.Sprintf("%02d_%dx%d%s", n+1, img.W, img.H, imageExt(img.URL))
			if err := os.WriteFile(filepath.Join(listingImgDir, name), body, 0o644); err == nil {
				stats["imagesDownloaded"] = stats["imagesDownloaded"].(int) + 1
			}
			sleepJitter(rng, opts.ImageDelayMin, opts.ImageDelayMax)
		}
	}
	if err := rows.Err(); err != nil {
		return stats, err
	}

	manifest, _ := json.MarshalIndent(stats, "", "  ")
	if err := os.WriteFile(filepath.Join(opts.OutDir, "run.json"), manifest, 0o644); err != nil {
		return stats, err
	}
	return stats, nil
}

func asInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int32:
		return int64(t), true
	case float64:
		return int64(t), true
	}
	return 0, false
}

func imageExt(u string) string {
	if i := strings.LastIndex(u, "."); i > 0 {
		ext := strings.ToLower(u[i:])
		if len(ext) <= 5 && strings.HasPrefix(ext, ".") {
			switch ext {
			case ".jpg", ".jpeg", ".png", ".webp", ".gif":
				return ext
			}
		}
	}
	return ".jpg"
}
