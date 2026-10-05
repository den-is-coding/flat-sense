// Package postgres — адаптер хранилища проекции ads (issue #5) поверх
// pgx/v5. Все запросы идемпотентны: событие parsed-ads доставляется
// at-least-once, повторный upsert по avito_id безопасен.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourusername/real-estate-analyzer/ads-service/internal/domain"
)

// Store — реализация domain.AdRepository на PostgreSQL.
// Пул создаётся лениво: при недоступной БД сервис стартует, а методы
// возвращают ошибки (консьюмер уложит сообщение в DLQ после ретраев,
// gRPC — codes.Unavailable).
type Store struct {
	pool *pgxpool.Pool
}

// NewStore — пул соединений поверх готового pgxpool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// NewPool — открыть пул. Соединения устанавливаются лениво, ошибка
// возвращается только при нечитаемом DSN; проверка связи — Ping.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, databaseURL)
}

// Close — закрыть пул (graceful shutdown).
func (s *Store) Close() { s.pool.Close() }

// Pool — доступ к пулу (для интеграционных тестов и служебных задач).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Ping — проверить связь с БД.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const adColumns = `avito_id, url, url_path, title, deal_type, category, price, price_currency,
	rooms, studio, total_area, floor, floors_total, address, city, district, metro,
	residential_complex, lat, lng, photos, description, request_id, created_at, updated_at`

// UpsertAd — создать или обновить объявление по avito_id
// (parsed_ad / CreateAd). created=true — вставка, false — обновление
// (трюк xmax=0: после DO UPDATE у конфликтной строки xmax != 0).
func (s *Store) UpsertAd(ctx context.Context, ad domain.Ad) (bool, error) {
	photos, err := json.Marshal(ad.Photos)
	if err != nil {
		return false, fmt.Errorf("postgres: marshal photos: %w", err)
	}
	var created bool
	err = s.pool.QueryRow(ctx, `
INSERT INTO ads (`+adColumns+`)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23, now(), now())
ON CONFLICT (avito_id) DO UPDATE SET
	url                 = EXCLUDED.url,
	url_path            = EXCLUDED.url_path,
	title               = EXCLUDED.title,
	deal_type           = EXCLUDED.deal_type,
	category            = EXCLUDED.category,
	price               = EXCLUDED.price,
	price_currency      = EXCLUDED.price_currency,
	rooms               = EXCLUDED.rooms,
	studio              = EXCLUDED.studio,
	total_area          = EXCLUDED.total_area,
	floor               = EXCLUDED.floor,
	floors_total        = EXCLUDED.floors_total,
	address             = EXCLUDED.address,
	city                = EXCLUDED.city,
	district            = EXCLUDED.district,
	metro               = EXCLUDED.metro,
	residential_complex = EXCLUDED.residential_complex,
	lat                 = EXCLUDED.lat,
	lng                 = EXCLUDED.lng,
	photos              = EXCLUDED.photos,
	description         = EXCLUDED.description,
	request_id          = EXCLUDED.request_id,
	updated_at          = now()
RETURNING xmax = 0`,
		ad.AvitoID, ad.URL, ad.URLPath, ad.Title, ad.DealType, ad.Category, ad.Price, ad.PriceCurrency,
		ad.Rooms, ad.Studio, ad.TotalArea, ad.Floor, ad.FloorsTotal, ad.Address, ad.City, ad.District,
		ad.Metro, ad.ResidentialComplex, ad.Lat, ad.Lng, photos, ad.Description, ad.RequestID,
	).Scan(&created)
	if err != nil {
		return false, fmt.Errorf("postgres: upsert ad %d: %w", ad.AvitoID, err)
	}
	return created, nil
}

// GetAd — объявление по avito_id; domain.ErrNotFound, если строки нет.
func (s *Store) GetAd(ctx context.Context, avitoID int64) (domain.Ad, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+adColumns+` FROM ads WHERE avito_id = $1`, avitoID)
	return scanAd(row.Scan)
}

// SimilarAds — эвристика «похожих» (issue #5, базовая версия):
//   - у исходного объявления заполнен residential_complex — соседи того же
//     ЖК, отсортированные по близости цены (NULL-цена исходного = 0);
//   - иначе тот же city + deal_type (опять же по близости цены);
//   - нет ни ЖК, ни города — пустой список.
//
// Объявления без цены пропускаются: «ближайшие по цене» для них не
// определены. limit нормализует gRPC-адаптер.
func (s *Store) SimilarAds(ctx context.Context, ad domain.Ad, limit int) ([]domain.Ad, error) {
	var (
		rows pgx.Rows
		err  error
	)
	switch {
	case ad.ResidentialComplex != "":
		rows, err = s.pool.Query(ctx, `
SELECT `+adColumns+` FROM ads
WHERE residential_complex = $1 AND avito_id <> $2 AND price IS NOT NULL
ORDER BY abs(price - $3), avito_id
LIMIT $4`, ad.ResidentialComplex, ad.AvitoID, derefI64(ad.Price), limit)
	case ad.City != "":
		rows, err = s.pool.Query(ctx, `
SELECT `+adColumns+` FROM ads
WHERE city = $1 AND deal_type = $2 AND avito_id <> $3 AND price IS NOT NULL
ORDER BY abs(price - $4), avito_id
LIMIT $5`, ad.City, ad.DealType, ad.AvitoID, derefI64(ad.Price), limit)
	default:
		return []domain.Ad{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: similar ads for %d: %w", ad.AvitoID, err)
	}
	defer rows.Close()

	out := []domain.Ad{}
	for rows.Next() {
		a, err := scanAd(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: similar ads for %d: %w", ad.AvitoID, err)
	}
	return out, nil
}

// SaveAnalysis — создать/перезаписать результат анализа по ad_id.
// Нарушение FK (объявления ещё нет в проекции) транслируется в
// domain.ErrNotFound, чтобы gRPC-слой ответил NotFound.
func (s *Store) SaveAnalysis(ctx context.Context, a domain.Analysis) error {
	if a.Payload == nil {
		a.Payload = []byte("{}")
	}
	if a.ComputedAt.IsZero() {
		a.ComputedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO analysis_results (ad_id, status, rent_forecast, yield_percent, payback_years, payload, computed_at)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (ad_id) DO UPDATE SET
	status         = EXCLUDED.status,
	rent_forecast  = EXCLUDED.rent_forecast,
	yield_percent  = EXCLUDED.yield_percent,
	payback_years  = EXCLUDED.payback_years,
	payload        = EXCLUDED.payload,
	computed_at    = EXCLUDED.computed_at`,
		a.AdID, a.Status, a.RentForecast, a.YieldPercent, a.PaybackYears, a.Payload, a.ComputedAt)
	if err != nil {
		if isForeignKey(err) {
			return fmt.Errorf("postgres: save analysis %d: %w", a.AdID, domain.ErrNotFound)
		}
		return fmt.Errorf("postgres: save analysis %d: %w", a.AdID, err)
	}
	return nil
}

// scanAd — развернуть строку ads в доменную модель (pgx сам умеет
// NULL → указатели, jsonb → []byte, timestamptz → time.Time).
func scanAd(scan func(dest ...any) error) (domain.Ad, error) {
	var (
		a      domain.Ad
		photos []byte
	)
	err := scan(&a.AvitoID, &a.URL, &a.URLPath, &a.Title, &a.DealType, &a.Category, &a.Price,
		&a.PriceCurrency, &a.Rooms, &a.Studio, &a.TotalArea, &a.Floor, &a.FloorsTotal, &a.Address,
		&a.City, &a.District, &a.Metro, &a.ResidentialComplex, &a.Lat, &a.Lng, &photos,
		&a.Description, &a.RequestID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Ad{}, domain.ErrNotFound
		}
		return domain.Ad{}, fmt.Errorf("postgres: scan ad: %w", err)
	}
	if len(photos) > 0 {
		if err := json.Unmarshal(photos, &a.Photos); err != nil {
			return domain.Ad{}, fmt.Errorf("postgres: unmarshal photos: %w", err)
		}
	}
	return a, nil
}

// isForeignKey — ошибка нарушения внешнего ключа (23503).
func isForeignKey(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func derefI64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
