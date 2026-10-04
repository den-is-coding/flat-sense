// Package source — адаптеры порта evaluate.Source: каталог дампов парсера
// (локальные прогоны и тесты на данных из директории) и БД avito_listings.
package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// DumpSource — источник из каталогов с дампами парсера. Понимает два
// формата: экспорт по объявлениям (data/<кампания>/listings/*.json) и
// кампейновые дампы аренды {meta, items:[...]} из
// parser-service/internal/avito/testdata/avito_run_arenda_*.json
// (у items площадь/этаж извлекаются из заголовка, дом — по адресу).
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
			switch {
			case skipFile(blob):
				continue
			case isCampaignDump(blob):
				for _, l := range parseCampaignDump(blob) {
					s.listings[l.ID] = l
				}
			default:
				l, err := evaluate.ParseListing(blob)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", e.Name(), err)
				}
				s.listings[l.ID] = l
			}
		}
	}
	return s, nil
}

// isCampaignDump — файл формата {meta, items}.
func isCampaignDump(blob []byte) bool {
	var probe struct {
		Meta  json.RawMessage   `json:"meta"`
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(blob, &probe); err != nil {
		return false
	}
	return probe.Meta != nil && probe.Items != nil
}

// skipFile — файл-артефакт кампании (массивы analysis.json/selection.json),
// не являющийся объявлением: пропускаем без ошибки.
func skipFile(blob []byte) bool {
	trimmed := strings.TrimSpace(string(blob))
	return strings.HasPrefix(trimmed, "[")
}

// campaignItem — item кампейнового дампа аренды.
type campaignItem struct {
	ID      int64  `json:"avito_id"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Address string `json:"address"`
	Metro   []struct {
		Station string `json:"station"`
	} `json:"metro"`
	Price       int64    `json:"price"`
	Description string   `json:"description"`
	Images      []string `json:"image_urls_1280"`
}

// parseCampaignDump превращает {meta, items} в объявления аренды
// (rent_long): кампании собираются по ЖК, площадь/этаж — в заголовке
// («Квартира-студия, 26 м², 10/12 эт.»).
func parseCampaignDump(blob []byte) []*evaluate.Listing {
	var d struct {
		Items []campaignItem `json:"items"`
	}
	if err := json.Unmarshal(blob, &d); err != nil {
		return nil
	}
	out := make([]*evaluate.Listing, 0, len(d.Items))
	for i := range d.Items {
		it := &d.Items[i]
		if it.ID == 0 || it.Price <= 0 {
			continue
		}
		// Авито вставляет NBSP (U+00A0) вместо пробелов в заголовках
		// («26\u00a0м²») — ломает регэкспы \s, нормализуем сразу.
		l := &evaluate.Listing{
			ID:          it.ID,
			URL:         it.URL,
			Title:       strings.ReplaceAll(it.Title, "\u00a0", " "),
			DealType:    "rent_long",
			Price:       it.Price,
			Address:     strings.ReplaceAll(it.Address, "\u00a0", " "),
			Description: it.Description,
		}
		if len(it.Metro) > 0 {
			l.Metro = it.Metro[0].Station
		}
		for _, u := range it.Images {
			l.Images = append(l.Images, evaluate.Image{URL: u})
		}
		area, floor, floorsTotal := parseTitleMetrics(l.Title)
		l.TotalArea = area
		if floor > 0 {
			l.Floor = &floor
		}
		if floorsTotal > 0 {
			l.FloorsTotal = &floorsTotal
		}
		out = append(out, l)
	}
	return out
}

var titleAreaRe = regexp.MustCompile(`(\d+([.,]\d+)?)\s*м²`)
var titleFloorRe = regexp.MustCompile(`(\d+)/(\d+)\s*эт`)

// parseTitleMetrics — площадь и этаж из заголовка объявления.
func parseTitleMetrics(title string) (area float64, floor, floorsTotal int) {
	if m := titleAreaRe.FindStringSubmatch(title); m != nil {
		area, _ = strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
	}
	if m := titleFloorRe.FindStringSubmatch(title); m != nil {
		floor, _ = strconv.Atoi(m[1])
		floorsTotal, _ = strconv.Atoi(m[2])
	}
	return area, floor, floorsTotal
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
