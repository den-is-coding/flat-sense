package avito

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Category — категория недвижимости на Авито (slug в URL).
type Category string

const (
	CatFlats      Category = "kvartiry"                    // квартиры
	CatRooms      Category = "komnaty"                     // комнаты
	CatHouses     Category = "doma_dachi_kottedzhi"        // дома, дачи, коттеджи
	CatLand       Category = "zemelnye_uchastki"           // земельные участки
	CatGarages    Category = "garazhi_i_mashinomesta"      // гаражи и машиноместа
	CatCommercial Category = "kommercheskaya_nedvizhimost" // коммерческая недвижимость
)

// DealType — тип сделки (segment в URL).
type DealType string

const (
	DealSale      DealType = "prodam"                 // продажа
	DealRentLong  DealType = "sdam_na_dlitelnyy_srok" // аренда на длительный срок (квартиры/комнаты)
	DealRentDaily DealType = "sdam_posutochno"        // аренда посуточно (квартиры/комнаты)
	DealRentPlain DealType = "sdam"                   // аренда для остальных категорий
)

// SortOrder — порядок выдачи (параметр s=).
type SortOrder string

const (
	SortDefault SortOrder = ""    // по умолчанию (релевантность)
	SortDate    SortOrder = "1"   // по дате
	SortCheaper SortOrder = "104" // дешевле
	SortPricier SortOrder = "126" // дороже
)

// SellerType — фильтр по типу продавца (user=).
type SellerType string

const (
	SellerAny     SellerType = ""
	SellerPrivate SellerType = "1" // частник
	SellerCompany SellerType = "2" // компания/агентство
)

// SearchFilters — поисковый запрос, повторяющий фильтры выдачи Авито.
// Поля, для которых у Авито нет классического URL-параметра (этажи, площадь и т.п.),
// передаются в Advanced — готовые слаги фильтра f=, скопированные из браузера:
// откройте выдачу с нужными фильтрами и перенесите значение f= целиком.
type SearchFilters struct {
	City     string   // slug города: moskva, sankt-peterburg, moskva_i_mo ...
	Category Category // kvartiry / komnaty / ...
	Deal     DealType // prodam / sdam_na_dlitelnyy_srok / sdam_posutochno / sdam

	Page  int    // номер страницы, 1-based (p=)
	Query string // поисковая строка (q=)

	PriceMin int64 // pmin=
	PriceMax int64 // pmax=

	Rooms []int // значения «Количество комнат»: 1..5+; 0 = студия (слаги f= для комнат)

	WithImageOnly bool       // только с фото
	SellerType    SellerType // частник/компания
	Sort          SortOrder

	// Advanced — компоненты параметра f= (slug-фильтры Авито: площадь, этажи,
	// тип дома, ремонт, балкон, санузел и пр.). Формат: "f=ASgBAgICA0TQgPl4xm..."
	// или отдельные слаги без префикса, они склеятся в один f=.
	Advanced []string

	// Extra — произвольные дополнительные query-параметры (e.g. send_timestamp, touch).
	Extra map[string]string
}

// Validate проверяет обязательные поля.
func (f *SearchFilters) Validate() error {
	if strings.TrimSpace(f.City) == "" {
		return fmt.Errorf("city is required (e.g. moskva)")
	}
	if f.Category == "" {
		return fmt.Errorf("category is required")
	}
	if f.Deal == "" {
		return fmt.Errorf("deal type is required")
	}
	return nil
}

// dealSegment подбирает slug аренды под категорию: у квартир и комнат
// аренда делится на «на длительный срок» и «посуточно», у остальных — общий «sdam».
func (f *SearchFilters) dealSegment() DealType {
	deal := f.Deal
	isFlatLike := f.Category == CatFlats || f.Category == CatRooms
	if !isFlatLike && deal == DealRentLong {
		deal = DealRentPlain
	}
	return deal
}

// BuildURL собирает URL страницы выдачи с учётом пагинации.
func (f *SearchFilters) BuildURL(page int) string {
	deal := f.dealSegment()
	u := fmt.Sprintf("https://www.avito.ru/%s/%s/%s", strings.Trim(f.City, "/"), f.Category, deal)

	q := url.Values{}
	if f.Query != "" {
		q.Set("q", f.Query)
	}
	if f.PriceMin > 0 {
		q.Set("pmin", strconv.FormatInt(f.PriceMin, 10))
	}
	if f.PriceMax > 0 {
		q.Set("pmax", strconv.FormatInt(f.PriceMax, 10))
	}
	if f.Sort != SortDefault {
		q.Set("s", string(f.Sort))
	}
	switch f.SellerType {
	case SellerPrivate:
		q.Set("user", "1")
	case SellerCompany:
		q.Set("user", "2")
	}
	if f.WithImageOnly {
		q.Set("withImagesOnly", "1")
	}
	if fslug := joinFSlobs(f.Advanced); fslug != "" {
		q.Set("f", fslug)
	}
	if page > 1 {
		q.Set("p", strconv.Itoa(page))
	}
	for k, v := range f.Extra {
		q.Set(k, v)
	}
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

// joinFSlobs склеивает слаги f= в один параметр: префикс "f=" срезается,
// слаги соединяются точкой (формат Авито).
func joinFSlobs(slugs []string) string {
	parts := make([]string, 0, len(slugs))
	for _, s := range slugs {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "f=")
		for _, p := range strings.Split(s, ".") {
			if p != "" {
				parts = append(parts, p)
			}
		}
	}
	return strings.Join(parts, ".")
}

// FiltersFromURL разбирает готовый URL выдачи Авито (скопированный из браузера
// со всеми фильтрами) в SearchFilters. Все параметры, которых нет в структуре
// (слаги f= и прочие), попадают в Advanced/Extra и сохраняются при построении URL.
func FiltersFromURL(raw string) (*SearchFilters, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if host != "www.avito.ru" && host != "avito.ru" && host != "m.avito.ru" {
		return nil, fmt.Errorf("not an avito url: %s", host)
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 2 {
		return nil, fmt.Errorf("expected /{city}/{category}/... path, got %s", u.Path)
	}
	f := &SearchFilters{City: segs[0], Category: Category(segs[1])}
	if len(segs) >= 3 {
		switch segs[2] {
		case "prodam", "kuplyu":
			f.Deal = DealSale
		case "sdam_na_dlitelnyy_srok":
			f.Deal = DealRentLong
		case "sdam_posutochno":
			f.Deal = DealRentDaily
		case "sdam":
			f.Deal = DealRentPlain
		default:
			// /moskva/kvartiry — поиск по категории без сегмента сделки
			f.Deal = DealSale
		}
	} else {
		f.Deal = DealSale
	}

	q := u.Query()
	f.Page, _ = strconv.Atoi(q.Get("p"))
	f.Query = q.Get("q")
	f.PriceMin, _ = strconv.ParseInt(q.Get("pmin"), 10, 64)
	f.PriceMax, _ = strconv.ParseInt(q.Get("pmax"), 10, 64)
	switch q.Get("s") {
	case "1":
		f.Sort = SortDate
	case "104":
		f.Sort = SortCheaper
	case "126":
		f.Sort = SortPricier
	}
	switch q.Get("user") {
	case "1":
		f.SellerType = SellerPrivate
	case "2":
		f.SellerType = SellerCompany
	}
	if v := q.Get("withImagesOnly"); v == "1" || v == "true" {
		f.WithImageOnly = true
	}
	if fs := q.Get("f"); fs != "" {
		f.Advanced = append(f.Advanced, fs)
	}
	f.Extra = map[string]string{}
	for k := range q {
		switch k {
		case "p", "q", "pmin", "pmax", "s", "user", "f", "withImagesOnly":
			continue
		}
		f.Extra[k] = q.Get(k)
	}
	if len(f.Extra) == 0 {
		f.Extra = nil
	}
	return f, nil
}
