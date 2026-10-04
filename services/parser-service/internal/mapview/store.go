// Package mapview — публичная карта объектов на OpenStreetMap (issue #73):
// bbox-выдача объявлений с координатами и метриками из кэша ad_roi_results
// (#64/#66, на лету не считается), SSR-страница и клиентская карта Leaflet.
package mapview

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MapFilters — фильтры bbox-выдачи (применяются на бэкенде, ДО
// кластеризации на клиенте: скрытые объекты не влияют на агрегаты).
type MapFilters struct {
	BBox     [4]float64 // minLng, minLat, maxLng, maxLat
	HasROI   bool       // только с данными о рентабельности
	YieldMin float64    // доходность ≥ X % (с мебелью, иначе без)
	PriceMax int64      // цена ≤ Z
	Rooms    string     // "" | studio | 1 | 2 | 3plus
	Limit    int
}

func (f *MapFilters) normalize() {
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 5000
	}
}

// buildQuery — SELECT выдачи и условия фильтров (значения — только через
// параметры). Объявления без координат не выдаются никогда.
func (f *MapFilters) buildQuery() (string, []any) {
	f.normalize()
	var where []string
	var args []any
	add := func(cond string, vals ...any) {
		// каждое вхождение $% — свой последовательный плейсхолдер
		for _, v := range vals {
			args = append(args, v)
			cond = strings.Replace(cond, "$%", fmt.Sprintf("$%d", len(args)), 1)
		}
		where = append(where, cond)
	}
	add("a.lat IS NOT NULL AND a.lng IS NOT NULL")
	add("a.lat BETWEEN $% AND $%", f.BBox[1], f.BBox[3])
	add("a.lng BETWEEN $% AND $%", f.BBox[0], f.BBox[2])
	if f.HasROI {
		where = append(where,
			"r.status = 'ok' AND (r.yield_furnished_pct IS NOT NULL OR r.yield_unfurnished_pct IS NOT NULL)")
	}
	if f.YieldMin > 0 {
		// доходность точки = с мебелью, при отсутствии — без мебели
		add("coalesce(r.yield_furnished_pct, r.yield_unfurnished_pct) >= $%", f.YieldMin)
	}
	if f.PriceMax > 0 {
		add("a.price <= $%", f.PriceMax)
	}
	switch f.Rooms {
	case "studio":
		add("a.studio = true")
	case "1", "2":
		add("a.rooms = $%", mustAtoi(f.Rooms))
	case "3plus":
		where = append(where, "a.rooms >= 3")
	}
	sql := "SELECT " + mapColumns + " FROM avito_listings a LEFT JOIN ad_roi_results r ON r.ad_id = a.id WHERE " +
		strings.Join(where, " AND ") + " ORDER BY a.id LIMIT " + strconv.Itoa(f.Limit)
	return sql, args
}

const mapColumns = `
a.id, a.url, a.title, a.deal_type, a.price, a.total_area, a.rooms, a.studio,
a.floor, a.floors_total, a.address, a.residential_complex, a.city, a.lat, a.lng,
a.images->0->>'url' AS photo,
r.yield_unfurnished_pct, r.yield_furnished_pct,
r.total_cost_unfurnished, r.total_cost_furnished, r.confidence`

// MapItem — точка карты (JSON-ответ).
type MapItem struct {
	ID                 int64    `json:"id"`
	URL                string   `json:"url"`
	Title              string   `json:"title"`
	DealType           string   `json:"dealType"`
	Price              *int64   `json:"price"`
	TotalArea          *float64 `json:"area"`
	Rooms              *int     `json:"rooms"`
	Studio             bool     `json:"studio"`
	Floor              *int     `json:"floor"`
	FloorsTotal        *int     `json:"floorsTotal"`
	Address            string   `json:"address"`
	ResidentialComplex string   `json:"complex"`
	City               string   `json:"city"`
	Lat                float64  `json:"lat"`
	Lng                float64  `json:"lng"`
	Photo              string   `json:"photo,omitempty"`
	// ROI — результат #64/#66 (NULL-поля = «—»)
	YieldUnfurnished   *float64 `json:"yieldUnfurnished,omitempty"`
	YieldFurnished     *float64 `json:"yieldFurnished,omitempty"`
	TotalCostUnfurn    *int64   `json:"totalCostUnfurnished,omitempty"`
	TotalCostFurnished *int64   `json:"totalCostFurnished,omitempty"`
	Confidence         string   `json:"confidence,omitempty"`
}

// MapPage — ответ выдачи: точки + индикатор пропущенного.
type MapPage struct {
	Items    []MapItem `json:"items"`
	Filtered bool      `json:"filtered"` // применены фильтры поверх bbox
}

// Store — read-only доступ для карты.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool} }

// List — точки в bbox с фильтрами.
func (s *Store) List(ctx context.Context, f MapFilters) ([]MapItem, error) {
	sql, args := f.buildQuery()
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("map listings: %w", err)
	}
	defer rows.Close()
	out := []MapItem{}
	for rows.Next() {
		var it MapItem
		var title, address, complex, city *string
		var price **int64
		var area **float64
		var rooms, floor, floorsTotal **int
		var photo *string
		var yieldU, yieldF *float64
		var costU, costF *int64
		var conf *string
		if err := rows.Scan(&it.ID, &it.URL, &title, &it.DealType, &price, &area,
			&rooms, &it.Studio, &floor, &floorsTotal, &address, &complex, &city,
			&it.Lat, &it.Lng, &photo, &yieldU, &yieldF, &costU, &costF, &conf); err != nil {
			return nil, err
		}
		derefInt64 := func(p **int64) *int64 {
			if p == nil || *p == nil {
				return nil
			}
			return *p
		}
		derefFloat := func(p **float64) *float64 {
			if p == nil || *p == nil {
				return nil
			}
			return *p
		}
		derefInt := func(p **int) *int {
			if p == nil || *p == nil {
				return nil
			}
			return *p
		}
		str := func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		}
		it.Title, it.Address = str(title), str(address)
		it.ResidentialComplex, it.City = str(complex), str(city)
		it.Photo = str(photo)
		it.Price, it.TotalArea = derefInt64(price), derefFloat(area)
		it.Rooms, it.Floor, it.FloorsTotal = derefInt(rooms), derefInt(floor), derefInt(floorsTotal)
		it.YieldUnfurnished, it.YieldFurnished = yieldU, yieldF
		it.TotalCostUnfurn, it.TotalCostFurnished = costU, costF
		if conf != nil {
			it.Confidence = *conf
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Counts — счётчики для SSR-оболочки (индексируемые тексты страницы):
// всего объявлений, с координатами (на карте), без координат (не показаны).
func (s *Store) Counts(ctx context.Context) (total, withCoords int64, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE lat IS NOT NULL AND lng IS NOT NULL)
		FROM avito_listings`).Scan(&total, &withCoords)
	return total, withCoords, err
}

func mustAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
