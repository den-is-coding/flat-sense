package admin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ListingFilters — фильтры таблицы объявлений.
type ListingFilters struct {
	City      string // точное совпадение
	Complex   string // точное совпадение (ЖК)
	Rooms     int    // > 0 — точное число комнат (студия = rooms 0 + studio)
	Studio    *bool  // фильтр «только студии / не студии»
	PriceMin  int64
	PriceMax  int64
	HasCoords *bool // есть/нет координаты (без координат не сопоставится в кластеризацию #55)
	Search    string
	SortBy    string // price | total_area | first_seen_at | last_seen_at | published_at
	SortDir   string // asc | desc
	Limit     int
	Page      int
}

var sortColumns = map[string]string{
	"price":         "price",
	"total_area":    "total_area",
	"first_seen_at": "first_seen_at",
	"last_seen_at":  "last_seen_at",
	"published_at":  "published_at",
}

func (f *ListingFilters) normalize() {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	if f.Page <= 0 {
		f.Page = 1
	}
	if _, ok := sortColumns[f.SortBy]; !ok {
		f.SortBy = "last_seen_at" // сортируемая колонка — только из белого списка
	}
	if f.SortDir != "asc" {
		f.SortDir = "desc"
	}
}

func (f *ListingFilters) offset() int { return (f.Page - 1) * f.Limit }

// buildQuery собирает SELECT списка и COUNT с общими условиями.
// Колонки/направление сортировки подставляются из белого списка, значения —
// только через параметры.
func (f *ListingFilters) buildQuery() (listSQL, countSQL string, args []any) {
	f.normalize()
	var where []string
	add := func(cond string, vals ...any) {
		args = append(args, vals...)
		ph := fmt.Sprintf("$%d", len(args)-len(vals)+1)
		where = append(where, strings.Replace(cond, "$%", ph, 1))
	}
	if f.City != "" {
		add("city = $%", f.City)
	}
	if f.Complex != "" {
		add("residential_complex = $%", f.Complex)
	}
	if f.Rooms > 0 {
		add("rooms = $%", f.Rooms)
	}
	if f.Studio != nil {
		add("studio = $%", *f.Studio)
	}
	if f.PriceMin > 0 {
		add("price >= $%", f.PriceMin)
	}
	if f.PriceMax > 0 {
		add("price <= $%", f.PriceMax)
	}
	if f.HasCoords != nil {
		if *f.HasCoords {
			where = append(where, "lat IS NOT NULL AND lng IS NOT NULL")
		} else {
			where = append(where, "(lat IS NULL OR lng IS NULL)")
		}
	}
	if f.Search != "" {
		args = append(args, "%"+f.Search+"%")
		where = append(where, fmt.Sprintf("(address ILIKE $%d OR residential_complex ILIKE $%d OR title ILIKE $%d)", len(args), len(args), len(args)))
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	order := fmt.Sprintf(" ORDER BY %s %s NULLS LAST", sortColumns[f.SortBy], strings.ToUpper(f.SortDir))
	listSQL = "SELECT " + listingColumns + " FROM avito_listings" + cond + order +
		fmt.Sprintf(" LIMIT %d OFFSET %d", f.Limit, f.offset())
	countSQL = "SELECT count(*) FROM avito_listings" + cond
	return listSQL, countSQL, args
}

// ListingRow — строка таблицы (read-only, JSON-ответ и HTML).
type ListingRow struct {
	ID                 int64    `json:"id"`
	URL                string   `json:"url"`
	Title              string   `json:"title"`
	Price              *int64   `json:"price"`
	PricePerUnit       *int64   `json:"pricePerUnit,omitempty"`
	PricePerM2         *int64   `json:"pricePerM2"`
	TotalArea          *float64 `json:"totalArea"`
	Rooms              *int     `json:"rooms"`
	Studio             bool     `json:"studio"`
	Floor              *int     `json:"floor"`
	FloorsTotal        *int     `json:"floorsTotal"`
	Address            string   `json:"address"`
	ResidentialComplex string   `json:"residentialComplex"`
	City               string   `json:"city"`
	District           string   `json:"district"`
	Metro              string   `json:"metro"`
	HouseType          string   `json:"houseType"`
	YearBuilt          *int     `json:"yearBuilt"`
	Renovation         string   `json:"renovation"`
	HasCoords          bool     `json:"hasCoords"`
	ImageCount         *int     `json:"imageCount"`
	PublishedAt        *string  `json:"publishedAt"`
	FirstSeenAt        string   `json:"firstSeenAt"`
	LastSeenAt         string   `json:"lastSeenAt"`
}

const listingColumns = `
id, url, title, price, price_per_unit, total_area, rooms, studio,
floor, floors_total, address, residential_complex, city, district, metro,
house_type, year_built, renovation, lat, lng, image_count,
published_at, first_seen_at, last_seen_at`

func scanListingRow(rs pgx.Rows) (ListingRow, error) {
	var r ListingRow
	// nullable колонки сканируются через двойные указатели (pgx)
	var title, address, complex, city, district, metro *string
	var houseType, renovation *string
	var price, pricePerUnit **int64
	var totalArea **float64
	var rooms, floor, floorsTotal, yearBuilt, imageCount **int
	var publishedAt, firstSeenAt, lastSeenAt *time.Time
	var lat, lng *float64
	err := rs.Scan(&r.ID, &r.URL, &title, &price, &pricePerUnit, &totalArea,
		&rooms, &r.Studio, &floor, &floorsTotal, &address,
		&complex, &city, &district, &metro,
		&houseType, &yearBuilt, &renovation, &lat, &lng, &imageCount,
		&publishedAt, &firstSeenAt, &lastSeenAt)
	if err != nil {
		return r, err
	}
	r.Title = derefStr(title)
	r.Price = deref(price)
	r.PricePerUnit = deref(pricePerUnit)
	r.TotalArea = deref(totalArea)
	r.Rooms = deref(rooms)
	r.Floor = deref(floor)
	r.FloorsTotal = deref(floorsTotal)
	r.Address = derefStr(address)
	r.ResidentialComplex = derefStr(complex)
	r.City = derefStr(city)
	r.District = derefStr(district)
	r.Metro = derefStr(metro)
	r.HouseType = derefStr(houseType)
	r.YearBuilt = deref(yearBuilt)
	r.Renovation = derefStr(renovation)
	r.ImageCount = deref(imageCount)
	r.HasCoords = lat != nil && lng != nil
	switch {
	case pricePerUnit != nil && *pricePerUnit != nil:
		v := **pricePerUnit
		r.PricePerM2 = &v // для квартир price_per_unit = цена за м²
	case r.Price != nil && r.TotalArea != nil && *r.TotalArea > 0:
		m2 := int64(float64(*r.Price) / *r.TotalArea)
		r.PricePerM2 = &m2
	}
	if publishedAt != nil {
		s := publishedAt.Format(time.RFC3339)
		r.PublishedAt = &s
	}
	r.FirstSeenAt = firstSeenAt.Format(time.RFC3339)
	r.LastSeenAt = lastSeenAt.Format(time.RFC3339)
	return r, nil
}

func deref[T any](p **T) *T {
	if p == nil || *p == nil {
		return nil
	}
	return *p
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ListingPage — страница списка.
type ListingPage struct {
	Total int          `json:"total"`
	Page  int          `json:"page"`
	Limit int          `json:"limit"`
	Items []ListingRow `json:"items"`
}

// Store — read-only доступ к avito_listings.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// AdminStore — интерфейс хранилища для обработчиков (позволяет подменять в тестах).
type AdminStore interface {
	List(ctx context.Context, f ListingFilters) (*ListingPage, error)
	Get(ctx context.Context, id int64) (*ListingRow, error)
}

// List — постраничная выдача объявлений с фильтрами и сортировкой.
func (s *Store) List(ctx context.Context, f ListingFilters) (*ListingPage, error) {
	f.normalize()
	listSQL, countSQL, args := f.buildQuery()
	var total int
	if err := s.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count listings: %w", err)
	}
	rows, err := s.pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("select listings: %w", err)
	}
	defer rows.Close()
	page := &ListingPage{Total: total, Page: f.Page, Limit: f.Limit, Items: []ListingRow{}}
	for rows.Next() {
		r, err := scanListingRow(rows)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, r)
	}
	return page, rows.Err()
}

// Get — одно объявление по avito id.
func (s *Store) Get(ctx context.Context, id int64) (*ListingRow, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+listingColumns+" FROM avito_listings WHERE id = $1", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err() // (nil, nil) — объявления с таким id нет
	}
	r, err := scanListingRow(rows)
	if err != nil {
		return nil, err
	}
	return &r, nil
}
