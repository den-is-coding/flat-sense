package avito

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Storage — PostgreSQL-хранилище объявлений (таблицы avito_listings,
// avito_parse_runs; схема в migrations/000010_avito_listings.up.sql).
type Storage struct {
	pool *pgxpool.Pool
}

func NewStorage(ctx context.Context, dsn string) (*Storage, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 8
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

// Pool — доступ к пулу соединений (для read-only админского хранилища #57).
func (s *Storage) Pool() *pgxpool.Pool { return s.pool }

// nilIfZero превращает нулевые значения в NULL для опциональных колонок.
func nilIfZero[T comparable](v T) any {
	var zero T
	if v == zero {
		return nil
	}
	return v
}

// UpsertListing вставляет или обновляет объявление.
// Возвращает (isNew, err): новая запись или обновление существующей.
func (s *Storage) UpsertListing(ctx context.Context, l *Listing) (bool, error) {
	if l.ID == 0 {
		return false, fmt.Errorf("listing id is required")
	}
	if l.Params == nil {
		l.Params = json.RawMessage("{}")
	}
	if l.Raw == nil {
		l.Raw = json.RawMessage("{}")
	}
	// Пустой Images → NULL (а не '[]'): иначе повторный импорт из
	// источника без картинок затирал бы уже сохранённые фото
	// (COALESCE(EXCLUDED.images, ...) ниже).
	var images any
	if len(l.Images) > 0 {
		images = mustJSON(l.Images)
	}

	q := `
INSERT INTO avito_listings (
    id, url, url_path, category, deal_type,
    title, description,
    price, price_currency, price_per_unit, price_unit, price_meta,
    rooms, studio, total_area, living_area, kitchen_area, land_area,
    floor, floors_total, house_type, renovation, balcony, bathroom, year_built,
    address, region, city, residential_complex, district, metro, lat, lng, geo,
    seller_name, seller_type, seller_url, seller_rating, seller,
    images, image_count,
    views_count, contacts_count, favorites_count,
    published_at, refreshed_at,
    params, raw, source_task,
    last_seen_at, updated_at
) VALUES (
    $1,$2,$3,$4,$5,
    $6,$7,
    $8,$9,$10,$11,$12,
    $13,$14,$15,$16,$17,$18,
    $19,$20,$21,$22,$23,$24,$25,
    $26,$27,$28,$29,$30,$31,$32,$33,$34,
    $35,$36,$37,$38,$39,
    $40,$41,
    $42,$43,$44,
    $45,$46,
    $47,$48,$49,
    now(), now()
)
ON CONFLICT (id) DO UPDATE SET
    url            = EXCLUDED.url,
    url_path       = EXCLUDED.url_path,
    title          = EXCLUDED.title,
    description    = COALESCE(NULLIF(EXCLUDED.description, ''), avito_listings.description),
    price          = COALESCE(EXCLUDED.price, avito_listings.price),
    price_currency = COALESCE(EXCLUDED.price_currency, avito_listings.price_currency),
    price_per_unit = COALESCE(EXCLUDED.price_per_unit, avito_listings.price_per_unit),
    price_unit     = COALESCE(EXCLUDED.price_unit, avito_listings.price_unit),
    price_meta     = COALESCE(EXCLUDED.price_meta, avito_listings.price_meta),
    rooms          = COALESCE(EXCLUDED.rooms, avito_listings.rooms),
    studio         = EXCLUDED.studio OR avito_listings.studio,
    total_area     = COALESCE(EXCLUDED.total_area, avito_listings.total_area),
    living_area    = COALESCE(EXCLUDED.living_area, avito_listings.living_area),
    kitchen_area   = COALESCE(EXCLUDED.kitchen_area, avito_listings.kitchen_area),
    land_area      = COALESCE(EXCLUDED.land_area, avito_listings.land_area),
    floor          = COALESCE(EXCLUDED.floor, avito_listings.floor),
    floors_total   = COALESCE(EXCLUDED.floors_total, avito_listings.floors_total),
    house_type     = COALESCE(EXCLUDED.house_type, avito_listings.house_type),
    renovation     = COALESCE(EXCLUDED.renovation, avito_listings.renovation),
    balcony        = COALESCE(EXCLUDED.balcony, avito_listings.balcony),
    bathroom       = COALESCE(EXCLUDED.bathroom, avito_listings.bathroom),
    year_built     = COALESCE(EXCLUDED.year_built, avito_listings.year_built),
    address        = COALESCE(EXCLUDED.address, avito_listings.address),
    region         = COALESCE(EXCLUDED.region, avito_listings.region),
    city           = COALESCE(EXCLUDED.city, avito_listings.city),
    residential_complex = COALESCE(EXCLUDED.residential_complex, avito_listings.residential_complex),
    district       = COALESCE(EXCLUDED.district, avito_listings.district),
    metro          = COALESCE(EXCLUDED.metro, avito_listings.metro),
    lat            = COALESCE(EXCLUDED.lat, avito_listings.lat),
    lng            = COALESCE(EXCLUDED.lng, avito_listings.lng),
    geo            = COALESCE(EXCLUDED.geo, avito_listings.geo),
    seller_name    = COALESCE(EXCLUDED.seller_name, avito_listings.seller_name),
    seller_type    = COALESCE(EXCLUDED.seller_type, avito_listings.seller_type),
    seller_url     = COALESCE(EXCLUDED.seller_url, avito_listings.seller_url),
    seller_rating  = COALESCE(EXCLUDED.seller_rating, avito_listings.seller_rating),
    seller         = COALESCE(EXCLUDED.seller, avito_listings.seller),
    images         = COALESCE(EXCLUDED.images, avito_listings.images),
    image_count    = COALESCE(EXCLUDED.image_count, avito_listings.image_count),
    views_count    = COALESCE(EXCLUDED.views_count, avito_listings.views_count),
    contacts_count = COALESCE(EXCLUDED.contacts_count, avito_listings.contacts_count),
    favorites_count= COALESCE(EXCLUDED.favorites_count, avito_listings.favorites_count),
    published_at   = COALESCE(EXCLUDED.published_at, avito_listings.published_at),
    refreshed_at   = COALESCE(EXCLUDED.refreshed_at, avito_listings.refreshed_at),
    params         = CASE WHEN EXCLUDED.params = '{}'::jsonb THEN avito_listings.params ELSE EXCLUDED.params END,
    raw            = EXCLUDED.raw,
    source_task    = COALESCE(EXCLUDED.source_task, avito_listings.source_task),
    last_seen_at   = now(),
    updated_at     = now()
RETURNING (xmax = 0) AS inserted`

	var inserted bool
	err := s.pool.QueryRow(ctx, q,
		l.ID, nilIfZero(l.URL), nilIfZero(l.URLPath), nilIfZero(l.Category), nilIfZero(string(l.DealType)),
		nilIfZero(l.Title), nilIfZero(l.Description),
		nilIfZero(l.Price), nilIfZero(l.Currency), nilIfZero(l.PricePerM2), nilIfZero(l.PriceUnit), jsonOrNil(l.PriceMeta),
		nilIfZero(l.Rooms), l.Studio, nilIfZero(l.TotalArea), nilIfZero(l.LivingArea), nilIfZero(l.KitchenArea), nilIfZero(l.LandArea),
		nilIfZero(l.Floor), nilIfZero(l.FloorsTotal), nilIfZero(l.HouseType), nilIfZero(l.Renovation), nilIfZero(l.Balcony), nilIfZero(l.Bathroom), nilIfZero(l.YearBuilt),
		nilIfZero(l.Address), nilIfZero(l.Region), nilIfZero(l.City), nilIfZero(l.ResidentialComplex), nilIfZero(l.District), nilIfZero(l.Metro), nilIfZero(l.Lat), nilIfZero(l.Lng), jsonOrNil(l.Geo),
		nilIfZero(l.SellerName), nilIfZero(l.SellerType), nilIfZero(l.SellerURL), nilIfZero(l.SellerRating), jsonOrNil(l.Seller),
		images, nilIfZero(l.ImageCount),
		nilIfZero(l.Views), nilIfZero(l.Contacts), nilIfZero(l.Favorites),
		timeOrNil(l.PublishedAt), timeOrNil(l.RefreshedAt),
		jsonOrNil(l.Params), l.Raw, nilIfZero(l.SourceTask),
	).Scan(&inserted)
	if err != nil {
		return false, fmt.Errorf("upsert listing %d: %w", l.ID, err)
	}
	l.IsNew = inserted
	return inserted, nil
}

func jsonOrNil(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

func timeOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// ParseRun — журнал запусков парсинга.
type ParseRun struct {
	ID           int64
	StartedAt    time.Time
	FinishedAt   *time.Time
	Filters      json.RawMessage
	RequestURL   string
	PagesFetched int
	ItemsFound   int
	ItemsNew     int
	ItemsUpdated int
	Status       string // running | ok | partial | error
	Error        string
}

func (s *Storage) CreateRun(ctx context.Context, filters json.RawMessage) (int64, error) {
	if len(filters) == 0 {
		filters = json.RawMessage("{}")
	}
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO avito_parse_runs (filters) VALUES ($1) RETURNING id`, []byte(filters)).Scan(&id)
	return id, err
}

func (s *Storage) FinishRun(ctx context.Context, run *ParseRun) error {
	now := time.Now()
	run.FinishedAt = &now
	_, err := s.pool.Exec(ctx, `
UPDATE avito_parse_runs SET
    finished_at=$2, request_url=$3, pages_fetched=$4,
    items_found=$5, items_new=$6, items_updated=$7,
    status=$8, error=$9
WHERE id=$1`,
		run.ID, now, run.RequestURL, run.PagesFetched,
		run.ItemsFound, run.ItemsNew, run.ItemsUpdated,
		run.Status, run.Error)
	return err
}

// ListRuns возвращает последние запуски парсинга.
func (s *Storage) ListRuns(ctx context.Context, limit int) ([]ParseRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
SELECT id, started_at, finished_at, filters, COALESCE(request_url, '') AS request_url,
       pages_fetched, items_found, items_new, items_updated, status, COALESCE(error, '') AS error
FROM avito_parse_runs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ParseRun
	for rows.Next() {
		var r ParseRun
		if err := rows.Scan(&r.ID, &r.StartedAt, &r.FinishedAt, &r.Filters, &r.RequestURL,
			&r.PagesFetched, &r.ItemsFound, &r.ItemsNew, &r.ItemsUpdated, &r.Status, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RawRow — строка avito_listings для пере-парсинга.
type RawRow struct {
	ID       int64
	Category string
	DealType string
	Raw      []byte
}

// SelectRawByTask отдаёт исходники объявлений задачи (для офлайн-пере-парсинга).
func (s *Storage) SelectRawByTask(ctx context.Context, task string) ([]RawRow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, category, deal_type, raw::text FROM avito_listings WHERE source_task=$1 ORDER BY id`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RawRow
	for rows.Next() {
		var r RawRow
		if err := rows.Scan(&r.ID, &r.Category, &r.DealType, &r.Raw); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteListing удаляет объявление (например, не прошедшее фильтры после обогащения).
func (s *Storage) DeleteListing(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM avito_listings WHERE id=$1`, id)
	return err
}
