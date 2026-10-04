// Package storage — PostgreSQL-хранилище вердиктов детекции планировок
// (таблица ad_floor_plans, миграция 000012; issue #58).
package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourusername/real-estate-analyzer/image-ai-service/internal/floorplan"
)

// Storage — доступ к таблице ad_floor_plans и фото объявления.
type Storage struct {
	pool *pgxpool.Pool
}

func NewStorage(ctx context.Context, dsn string) (*Storage, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Storage{pool: pool}, nil
}

func (s *Storage) Close() { s.pool.Close() }

// Record — строка ad_floor_plans.
type Record struct {
	AdID       int64   `json:"adId"`
	PhotoRef   string  `json:"photoRef"`
	IsPlan     bool    `json:"isPlan"`
	Confidence float64 `json:"confidence"`
	Method     string  `json:"method"`
	CheckedAt  string  `json:"checkedAt,omitempty"`
}

// CheckedRefs возвращает множество уже проверенных фото объявления —
// повторная обработка их не пересматривает.
func (s *Storage) CheckedRefs(ctx context.Context, adID int64) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT photo_ref FROM ad_floor_plans WHERE ad_id = $1`, adID)
	if err != nil {
		return nil, fmt.Errorf("select checked: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		out[ref] = true
	}
	return out, rows.Err()
}

// SaveVerdict пишет вердикт по одному фото (идемпотентно: повторная
// проверка того же фото обновляет вердикт и checked_at).
func (s *Storage) SaveVerdict(ctx context.Context, adID int64, photoRef string, v floorplan.Verdict) error {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO ad_floor_plans (ad_id, photo_ref, is_plan, confidence, method)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (ad_id, photo_ref)
		DO UPDATE SET is_plan = EXCLUDED.is_plan,
		              confidence = EXCLUDED.confidence,
		              method = EXCLUDED.method,
		              checked_at = now()`,
		adID, photoRef, v.IsPlan, v.Confidence, v.Method)
	if err != nil {
		return fmt.Errorf("save verdict: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("save verdict: no rows affected")
	}
	return nil
}

// MainPlan возвращает основную планировку объявления (максимальная
// confidence среди is_plan) или nil, если планировки нет.
func (s *Storage) MainPlan(ctx context.Context, adID int64) (*Record, error) {
	r := &Record{}
	err := s.pool.QueryRow(ctx, `
		SELECT ad_id, photo_ref, is_plan, confidence, method, checked_at
		FROM ad_floor_plans
		WHERE ad_id = $1 AND is_plan
		ORDER BY confidence DESC
		LIMIT 1`, adID).
		Scan(&r.AdID, &r.PhotoRef, &r.IsPlan, &r.Confidence, &r.Method, &r.CheckedAt)
	if err == nil {
		return r, nil
	}
	// «Не найдено» — нормальный ответ (планировки нет), не ошибка.
	return nil, nil
}

// AdImages возвращает ссылки на фото объявления из avito_listings.images
// (JSONB [{url, w, h}]) и заголовок объявления.
func (s *Storage) AdImages(ctx context.Context, adID int64) ([]string, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT images FROM avito_listings WHERE id = $1`, adID).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("select listing %d: %w", adID, err)
	}
	var imgs []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &imgs); err != nil {
		return nil, fmt.Errorf("parse images jsonb: %w", err)
	}
	out := make([]string, 0, len(imgs))
	for _, im := range imgs {
		if im.URL != "" {
			out = append(out, im.URL)
		}
	}
	return out, nil
}
