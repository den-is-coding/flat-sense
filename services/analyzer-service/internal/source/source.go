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
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/zhk"
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
	Coordinates *struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	} `json:"coordinates"`
}

// parseCampaignDump превращает {meta, items} в объявления аренды
// (rent_long). Кампания собирается по одному ЖК (несколько корпусов), поэтому
// каждому item выдаются алиасы-ключи по адресам ВСЕХ корпусов кампании —
// так sale-объявление любого корпуса кластеризуется на уровне ЖК
// (issue #64: «в этом же ЖК»). Координаты — для радиус-фолбэка.
func parseCampaignDump(blob []byte) []*evaluate.Listing {
	var d struct {
		Meta struct {
			Addresses map[string]int `json:"addresses"`
		} `json:"meta"`
		Items []campaignItem `json:"items"`
	}
	if err := json.Unmarshal(blob, &d); err != nil {
		return nil
	}
	// Адреса корпусов ЖК: из meta.addresses, иначе — множество адресов items.
	var zhkAddrs []string
	for a := range d.Meta.Addresses {
		zhkAddrs = append(zhkAddrs, a)
	}
	if len(zhkAddrs) == 0 {
		seen := map[string]bool{}
		for i := range d.Items {
			a := strings.TrimSpace(d.Items[i].Address)
			if a != "" && !seen[a] {
				seen[a] = true
				zhkAddrs = append(zhkAddrs, a)
			}
		}
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
			Aliases:     zhkAddrs,
		}
		if it.Coordinates != nil {
			lat, lng := it.Coordinates.Lat, it.Coordinates.Lng
			l.Lat, l.Lng = &lat, &lng
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

func (s *DBSource) listingJSON() string {
	return `
		SELECT jsonb_build_object(
			'id', id, 'url', url, 'title', title, 'deal_type', deal_type,
			'category', category, 'rooms', rooms, 'studio', studio,
			'total_area', total_area, 'floor', floor, 'floors_total', floors_total,
			'price', price, 'address', address, 'city', city, 'district', district,
			'metro', metro, 'house_type', house_type, 'residential_complex', residential_complex,
			'description', description,
			'params', params, 'images', images, 'geo', geo, 'lat', lat, 'lng', lng
		)`
}

func (s *DBSource) ListingByID(ctx context.Context, id int64) (*evaluate.Listing, error) {
	var blob []byte
	err := s.pool.QueryRow(ctx, s.listingJSON()+` FROM avito_listings WHERE id = $1`, id).Scan(&blob)
	if err != nil {
		return nil, fmt.Errorf("listing %d: %w", id, err)
	}
	l, err := evaluate.ParseListing(blob)
	if err != nil {
		return nil, err
	}
	// Алиасы ЖК (реестр zhk, issue #66): кластеризация уровня ЖК — как
	// с meta.addresses кампаний в DumpSource.
	zhk.Enrich(l)
	return l, nil
}

func (s *DBSource) RentListings(ctx context.Context) ([]evaluate.Listing, error) {
	rows, err := s.pool.Query(ctx, s.listingJSON()+
		`, coalesce(source_task, '') FROM avito_listings WHERE deal_type = 'rent_long'`)
	if err != nil {
		return nil, fmt.Errorf("rent listings: %w", err)
	}
	defer rows.Close()
	type keyed struct {
		l      evaluate.Listing
		task   string
		rawAdr string
	}
	var out []keyed
	for rows.Next() {
		var blob []byte
		var task string
		if err := rows.Scan(&blob, &task); err != nil {
			return nil, err
		}
		l, err := evaluate.ParseListing(blob)
		if err != nil {
			return nil, err
		}
		zhk.Enrich(l)
		out = append(out, keyed{l: *l, task: task, rawAdr: l.Address})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Алиасы корпусов ЖК по кампании (source_task): арендная кампания
	// собирается по одному ЖК, поэтому каждому объявлению выдаются адреса
	// всех корпусов его задачи — аналог meta.addresses дамп-адаптера.
	addrs := map[string][]string{}
	seen := map[string]map[string]bool{}
	for _, k := range out {
		a := strings.TrimSpace(k.rawAdr)
		if a == "" {
			continue
		}
		if seen[k.task] == nil {
			seen[k.task] = map[string]bool{}
		}
		if !seen[k.task][a] {
			seen[k.task][a] = true
			addrs[k.task] = append(addrs[k.task], a)
		}
	}
	res := make([]evaluate.Listing, 0, len(out))
	for _, k := range out {
		k.l.Aliases = addrs[k.task]
		res = append(res, k.l)
	}
	return res, nil
}

// SaleListings — весь пул продаж (вход backfill #66).
func (s *DBSource) SaleListings(ctx context.Context) ([]evaluate.Listing, error) {
	return s.listingsByDealType(ctx, "sale")
}

func (s *DBSource) listingsByDealType(ctx context.Context, dealType string) ([]evaluate.Listing, error) {
	rows, err := s.pool.Query(ctx, s.listingJSON()+
		` FROM avito_listings WHERE deal_type = $1`, dealType)
	if err != nil {
		return nil, fmt.Errorf("%s listings: %w", dealType, err)
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
		zhk.Enrich(l)
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
