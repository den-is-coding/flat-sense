package backfill

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// Интеграционный тест: живой PostgreSQL с миграциями 000010 и 000014.
// Проверяет прогон Runner и идемпотентность (повторный запуск не создаёт
// дублей — критерий приёмки #66).
func TestRun_Idempotent(t *testing.T) {
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		t.Skip("TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	seed := []struct {
		id          int64
		dealType    string
		price       int64
		area        float64
		address     string
		description string
	}{
		// ЖК Граффити: продажа в 42к2 и аренда в 42к1 — кластер уровня ЖК
		// собирается только через алиасы реестра zhk.
		{9001000001, "sale", 7_600_000, 24.0, "Санкт-Петербург, Парашютная ул., 42к2", "Студия без мебели."},
		{9001000002, "sale", 7_900_000, 26.0, "Санкт-Петербург, Лиговский пр., 50", "Студия."},
		{9001000003, "rent_long", 30_000, 21.0, "Санкт-Петербург, Парашютная ул., 42к1", "Студия без мебели, сдаём надолго."},
		{9001000004, "rent_long", 34_000, 22.0, "Санкт-Петербург, Парашютная ул., 42к3", "Студия с мебелью и техникой."},
	}
	for _, s := range seed {
		if _, err := pool.Exec(ctx, `DELETE FROM avito_listings WHERE id=$1`, s.id); err != nil {
			t.Fatal(err)
		}
		defer pool.Exec(ctx, `DELETE FROM avito_listings WHERE id=$1`, s.id)
		_, err := pool.Exec(ctx, `
			INSERT INTO avito_listings (id, url, url_path, category, deal_type, title, price,
			                            studio, total_area, address, description, raw, params)
			VALUES ($1, $2, $2, 'kvartiry', $3, 'Квартира-студия', $4, true, $5, $6, $7, '{}'::jsonb, '{}'::jsonb)`,
			s.id, "https://www.avito.ru/spb/kvartiry/backfill_test_"+string(rune('0'+s.id%10)),
			s.dealType, s.price, s.area, s.address, s.description)
		if err != nil {
			t.Fatalf("seed %d: %v", s.id, err)
		}
		defer func(id int64) {
			pool.Exec(ctx, `DELETE FROM ad_roi_results WHERE ad_id=$1`, id)
		}(s.id)
	}

	run := &Runner{Source: NewDBTestSource(pool), Pool: pool, Config: evaluate.DefaultConfig()}
	rep, err := run.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Computed == 0 {
		t.Fatalf("expected computed rows, report: %+v", rep)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ad_roi_results WHERE ad_id IN ($1,$2)`,
		seed[0].id, seed[1].id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("ad_roi_results rows = %d, want 2", n)
	}

	// Граффити посчитан, Лиговский — no_rent_data.
	var grafStatus, ligovStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM ad_roi_results WHERE ad_id=$1`, seed[0].id).Scan(&grafStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM ad_roi_results WHERE ad_id=$1`, seed[1].id).Scan(&ligovStatus); err != nil {
		t.Fatal(err)
	}
	if grafStatus != "ok" {
		t.Fatalf("graffiti status = %q, want ok (report: %+v)", grafStatus, rep)
	}
	if ligovStatus != "no_rent_data" {
		t.Fatalf("ligovsky status = %q, want no_rent_data", ligovStatus)
	}

	// Повторный прогон: дублей нет, статус пересчитывается.
	if _, err := run.Run(ctx); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ad_roi_results WHERE ad_id IN ($1,$2)`,
		seed[0].id, seed[1].id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("after re-run rows = %d, want 2 (no duplicates)", n)
	}

	// Сценарные цифры Граффити: аренда без мебели 30 000 (медиана группы
	// из одного) → доходность = 30000*12 / (7_600_000 + издержки) * 100.
	var yieldU *float64
	var costU *int64
	if err := pool.QueryRow(ctx,
		`SELECT yield_unfurnished_pct, total_cost_unfurnished FROM ad_roi_results WHERE ad_id=$1`,
		seed[0].id).Scan(&yieldU, &costU); err != nil {
		t.Fatal(err)
	}
	if yieldU == nil || costU == nil {
		t.Fatalf("graffiti scenario values nil: %v %v", yieldU, costU)
	}
	cfg := evaluate.DefaultConfig()
	wantCost := seed[0].price + cfg.DealCosts(seed[0].price)
	if *costU != wantCost {
		t.Fatalf("total_cost_unfurnished = %d, want %d", *costU, wantCost)
	}
	wantYield := 30_000 * 12 / float64(wantCost) * 100
	if diff := *yieldU - wantYield; diff > 0.01 || diff < -0.01 {
		t.Fatalf("yield = %.3f, want %.3f", *yieldU, wantYield)
	}
}

// dbTestSource — backfill.Source на живом пуле (тот же SQL, что DBSource).
type dbTestSource struct{ pool *pgxpool.Pool }

func NewDBTestSource(pool *pgxpool.Pool) Source { return &dbTestSource{pool} }

func (s *dbTestSource) SaleListings(ctx context.Context) ([]evaluate.Listing, error) {
	return s.byType(ctx, "sale")
}

func (s *dbTestSource) RentListings(ctx context.Context) ([]evaluate.Listing, error) {
	return s.byType(ctx, "rent_long")
}

func (s *dbTestSource) byType(ctx context.Context, dealType string) ([]evaluate.Listing, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT jsonb_build_object(
			'id', id, 'url', url, 'title', title, 'deal_type', deal_type,
			'category', category, 'rooms', rooms, 'studio', studio,
			'total_area', total_area, 'price', price, 'address', address,
			'description', description, 'params', params, 'images', images,
			'geo', geo, 'lat', lat, 'lng', lng
		) FROM avito_listings WHERE deal_type = $1`, dealType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []evaluate.Listing
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, err
		}
		l, err := evaluate.ParseListing(blob)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}
