package avito

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// reTitleArea / reTitleFloor — площадь и этаж из заголовка объявления
// («…, 27,7 м², 1/4 эт.»); NBSP уже нормализован в пробел.
var (
	reTitleArea  = regexp.MustCompile(`(\d+(?:[.,]\d+)?)\s*м²`)
	reTitleFloor = regexp.MustCompile(`(\d+)\s*/\s*(\d+)\s*эт`)
)

// ImportDump — дамп кампании ручного/драйверного сбора
// (см. PARSING_PLAYBOOK.md): {meta, items: [...]}.
type ImportDump struct {
	Meta  json.RawMessage `json:"meta"`
	Items []ImportItem    `json:"items"`
}

// ImportItem — одна карточка из дампа (hydration buyerItem, поля
// нормализованы драйвером сбора; отсутствующие поля = null).
type ImportItem struct {
	AvitoID        int64  `json:"avito_id"`
	URL            string `json:"url"`
	Title          string `json:"title"`
	Price          int64  `json:"price"`
	PriceFormatted string `json:"price_formatted"`
	Address        string `json:"address"`
	Coordinates    *struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	} `json:"coordinates"`
	Metro []struct {
		Station string `json:"station"`
	} `json:"metro"`
	HouseParams map[string]string `json:"house_params"`
	Description string            `json:"description"`
	ImageURLs   []string          `json:"image_urls_1280"`
	ImagesCount int               `json:"images_count"`
	Published   string            `json:"published"`
	// developer приходит в трёх видах: строка, {id, name} или null
	Developer         json.RawMessage `json:"developer"`
	SellerIsDeveloper bool            `json:"seller_is_developer"`
	Breadcrumbs       []string        `json:"breadcrumbs"`
	FinishTimeUnix    int64           `json:"finish_time_unix"`

	// сырой JSON карточки — сохраняется как есть
	Raw json.RawMessage `json:"-"`
}

// cityFromURLPath — человекочитаемый город из первого сегмента URL
// (/sankt-peterburg/kvartiry/... → Санкт-Петербург).
func cityFromURLPath(path string) string {
	seg := strings.TrimPrefix(path, "/")
	if i := strings.Index(seg, "/"); i >= 0 {
		seg = seg[:i]
	}
	switch seg {
	case "sankt-peterburg":
		return "Санкт-Петербург"
	case "moskva":
		return "Москва"
	default:
		return ""
	}
}

// yearFromHandover — «Сдача в 4 кв. 2026» / «Сдан в 2023» → год.
func yearFromHandover(v string) int {
	for _, f := range strings.Fields(v) {
		if n, err := strconv.Atoi(strings.Trim(f, ".,")); err == nil && n >= 1950 && n <= 2100 {
			return n
		}
	}
	return 0
}

// developerName — строка, поле name объекта или пусто (null).
func developerName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return o.Name
	}
	return ""
}

// listingFromImportItem — маппинг карточки дампа в Listing.
func listingFromImportItem(it ImportItem, sourceTask string, kind DealKind) (*Listing, error) {
	if it.AvitoID == 0 {
		return nil, fmt.Errorf("avito_id is required")
	}
	l := &Listing{
		ID:         it.AvitoID,
		URL:        "https://www.avito.ru" + it.URL,
		URLPath:    it.URL,
		Category:   "kvartiry",
		DealType:   kind,
		Title:      it.Title,
		Price:      it.Price,
		Currency:   "RUB",
		Address:    it.Address,
		City:       cityFromURLPath(it.URL),
		Raw:        it.Raw,
		SourceTask: sourceTask,
	}
	if it.Description != "" {
		l.Description = it.Description
	}
	// Заголовок вида «Квартира-студия, 27,7 м², 1/4 эт.» (NBSP → пробел);
	// площадь и этаж берём regex'ом — запятая внутри числа ломает Split по ","
	t := strings.ReplaceAll(it.Title, "\u00a0", " ")
	if strings.Contains(strings.ToLower(t), "студия") {
		l.Studio = true
	}
	if m := reTitleArea.FindStringSubmatch(t); m != nil {
		if a, err := strconv.ParseFloat(strings.Replace(m[1], ",", ".", 1), 64); err == nil {
			l.TotalArea = a
		}
	}
	if m := reTitleFloor.FindStringSubmatch(t); m != nil {
		l.Floor, _ = strconv.Atoi(m[1])
		l.FloorsTotal, _ = strconv.Atoi(m[2])
	}
	if l.TotalArea > 0 && l.Price > 0 {
		l.PricePerM2 = int64(float64(l.Price) / l.TotalArea)
		l.PriceUnit = "м2"
	}
	// Гео
	if it.Coordinates != nil && it.Coordinates.Lat != 0 {
		l.Lat = it.Coordinates.Lat
		l.Lng = it.Coordinates.Lng
	}
	var metroNames, district []string
	for _, m := range it.Metro {
		if s := strings.TrimSpace(m.Station); s != "" {
			if strings.Contains(strings.ToLower(s), "р-н") {
				district = append(district, s)
			} else {
				metroNames = append(metroNames, s)
			}
		}
	}
	l.Metro = strings.Join(metroNames, ", ")
	l.District = strings.Join(district, ", ")
	// Параметры дома
	if hp := it.HouseParams; hp != nil {
		l.ResidentialComplex = hp["Название новостройки"]
		l.HouseType = hp["Тип дома"]
		if v := hp["Этажей в доме"]; v != "" && l.FloorsTotal == 0 {
			if n, err := strconv.Atoi(leadingInt(v)); err == nil {
				l.FloorsTotal = n
			}
		}
		if v := hp["Срок сдачи"]; v != "" {
			l.YearBuilt = yearFromHandover(v)
		}
		// сохраняем всё в params в стандартном для avito_listings виде
		var params []Param
		for k, v := range hp {
			params = append(params, Param{Title: k, Value: v})
		}
		if b, err := json.Marshal(params); err == nil {
			l.Params = b
		}
	}
	// Продавец
	if name := developerName(it.Developer); name != "" {
		l.SellerName = name
		if it.SellerIsDeveloper {
			l.SellerType = "developer"
		}
	}
	// Медиа
	l.ImageCount = it.ImagesCount
	for _, u := range it.ImageURLs {
		l.Images = append(l.Images, Image{URL: u})
	}
	if l.Params == nil {
		l.Params = json.RawMessage("{}")
	}
	if l.Raw == nil {
		l.Raw = json.RawMessage("{}")
	}
	return l, nil
}

// dealKindFromQuery — тип сделки из запроса кампании (meta.query):
// «аренда (длительный срок)» → rent_long, «посуточно» → rent_daily,
// остальное — продажа.
func dealKindFromQuery(query string) DealKind {
	q := strings.ToLower(strings.ReplaceAll(query, "\u00a0", " "))
	switch {
	case strings.Contains(q, "посуточно"):
		return KindRentDaily
	case strings.Contains(q, "аренда"):
		return KindRentLong
	default:
		return KindSale
	}
}

// ImportListings разбирает дамп кампании и возвращает готовые к upsert записи.
// Карточки без avito_id пропускаются; исходный JSON каждой карточки
// сохраняется в Listing.Raw.
func ImportListings(data []byte, sourceTask string) ([]*Listing, error) {
	var dump ImportDump
	if err := json.Unmarshal(data, &dump); err != nil {
		return nil, fmt.Errorf("parse dump: %w", err)
	}
	var meta struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(dump.Meta, &meta)
	kind := dealKindFromQuery(meta.Query)
	var rawItems []json.RawMessage
	if err := json.Unmarshal(data, &struct {
		Items *[]json.RawMessage `json:"items"`
	}{&rawItems}); err != nil {
		rawItems = nil
	}
	var out []*Listing
	for idx, it := range dump.Items {
		if idx < len(rawItems) {
			it.Raw = rawItems[idx]
		}
		l, err := listingFromImportItem(it, sourceTask, kind)
		if err != nil {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}
