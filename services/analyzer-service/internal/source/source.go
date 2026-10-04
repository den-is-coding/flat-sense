// Package source — адаптеры порта evaluate.Source: каталог дампов парсера
// (локальные прогоны и тесты на данных из директории) и БД avito_listings.
package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// DumpSource — источник из каталогов с JSON-дампами парсера
// (data/<кампания>/listings/*.json). Только для тестов и локальных прогонов.
type DumpSource struct {
	listings map[int64]*evaluate.Listing
}

// NewDumpSource читает все *.json из перечисленных каталогов как объявления.
func NewDumpSource(dirs ...string) (*DumpSource, error) {
	s := &DumpSource{listings: map[int64]*evaluate.Listing{}}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read dir %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
				continue
			}
			blob, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			l, err := evaluate.ParseListing(blob)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", e.Name(), err)
			}
			s.listings[l.ID] = l
		}
	}
	return s, nil
}

func (s *DumpSource) ListingByID(_ context.Context, id int64) (*evaluate.Listing, error) {
	l, ok := s.listings[id]
	if !ok {
		return nil, fmt.Errorf("listing %d not found in dump", id)
	}
	return l, nil
}

func (s *DumpSource) RentListings(_ context.Context) ([]evaluate.Listing, error) {
	var out []evaluate.Listing
	for _, l := range s.listings {
		if l.DealType == "rent_long" {
			out = append(out, *l)
		}
	}
	return out, nil
}

// DBSource — источник из PostgreSQL (avito_listings, миграция 000010).
type DBSource struct {
	pool *pgxpool.Pool
}

func NewDBSource(pool *pgxpool.Pool) *DBSource { return &DBSource{pool: pool} }

func (s *DBSource) ListingByID(ctx context.Context, id int64) (*evaluate.Listing, error) {
	var blob []byte
	err := s.pool.QueryRow(ctx, `
		SELECT jsonb_build_object(
			'id', id, 'url', url, 'title', title, 'deal_type', deal_type,
			'category', category, 'rooms', rooms, 'studio', studio,
			'total_area', total_area, 'floor', floor, 'floors_total', floors_total,
			'price', price, 'address', address, 'city', city, 'district', district,
			'metro', metro, 'house_type', house_type, 'description', description,
			'params', params, 'images', images, 'geo', geo
		) FROM avito_listings WHERE id = $1`, id).Scan(&blob)
	if err != nil {
		return nil, fmt.Errorf("listing %d: %w", id, err)
	}
	return evaluate.ParseListing(blob)
}

func (s *DBSource) RentListings(ctx context.Context) ([]evaluate.Listing, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT jsonb_build_object(
			'id', id, 'url', url, 'title', title, 'deal_type', deal_type,
			'category', category, 'rooms', rooms, 'studio', studio,
			'total_area', total_area, 'floor', floor, 'floors_total', floors_total,
			'price', price, 'address', address, 'city', city, 'district', district,
			'metro', metro, 'house_type', house_type, 'description', description,
			'params', params, 'images', images, 'geo', geo
		) FROM avito_listings WHERE deal_type = 'rent_long'`)
	if err != nil {
		return nil, fmt.Errorf("rent listings: %w", err)
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

// ExtractListingID — id объявления из URL Авито (…_3651684187?…) или
// из строки-числа. Возвращает 0, если id не найден.
func ExtractListingID(s string) int64 {
	s = strings.TrimRight(strings.Split(s, "?")[0], "/")
	seg := s[strings.LastIndex(s, "/")+1:]
	if i := strings.LastIndex(seg, "_"); i >= 0 {
		seg = seg[i+1:]
	}
	id, err := strconv.ParseInt(seg, 10, 64)
	if err != nil {
		return 0
	}
	return id
}
