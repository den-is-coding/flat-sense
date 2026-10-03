package avito

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseSearchPage разбирает HTML страницы выдачи и возвращает объявления.
// f может быть nil — тогда категория/тип сделки из страницы не проставляются.
func ParseSearchPage(pageHTML string, f *SearchFilters) ([]*Listing, error) {
	states, err := extractStates(pageHTML)
	if err != nil {
		return nil, err
	}
	items := findItemsArray(states)
	if items == nil {
		// Состояние есть, выдачи нет: либо пустой фильтр, либо «отравленный»
		// ответ антибота — валидируем это на уровне сервиса.
		return nil, nil
	}
	var out []*Listing
	for _, it := range items {
		m, ok := asMap(it)
		if !ok {
			continue
		}
		l := parseSearchItem(m)
		if l == nil {
			continue
		}
		if f != nil {
			l.Category = string(f.Category)
			l.DealType = dealKind(f)
		}
		out = append(out, l)
	}
	return out, nil
}

// dealKind нормализует DealType → DealKind для БД.
func dealKind(f *SearchFilters) DealKind {
	switch f.dealSegment() {
	case DealRentLong, DealRentDaily:
		if f.dealSegment() == DealRentDaily {
			return KindRentDaily
		}
		return KindRentLong
	case DealRentPlain:
		return KindRentLong
	default:
		return KindSale
	}
}

func parseSearchItem(m map[string]any) *Listing {
	idStr := mapStr(m, "id")
	if idStr == "" {
		idStr = mapStr(m, "itemId")
	}
	if idStr == "" {
		return nil
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		return nil
	}

	l := &Listing{ID: id}
	l.URLPath = mapStr(m, "urlPath")
	if l.URLPath == "" {
		l.URLPath = mapStr(m, "url")
	}
	l.URL = absoluteURL(l.URLPath)
	l.Title = mapStr(m, "title")

	if pd, ok := deepGet(m, "priceDetailed"); ok {
		var p priceDetailed
		if b, err := json.Marshal(pd); err == nil && json.Unmarshal(b, &p) == nil {
			l.SetPriceFromDetailed(&p)
			l.PriceMeta = b
		}
	} else if v, ok := asFloat(mapGet(m, "price")); ok {
		l.Price = int64(v)
		l.Currency = "RUB"
	}

	if params, ok := asArray(mapGet(m, "params")); ok {
		var pms []map[string]any
		for _, p := range params {
			if pm, ok := asMap(p); ok {
				pms = append(pms, pm)
			}
		}
		applyParams(l, pms)
		l.Params = mustJSON(pms)
	}

	l.Address = mapStr(m, "address")
	parseGeo(l, m)
	parseImages(l, m)
	applySearchIVA(l, m)

	if t := parseEpochMs(mapGet(m, "time")); !t.IsZero() {
		l.PublishedAt = t
	} else if t := parseEpochMs(mapGet(m, "publishedAt")); !t.IsZero() {
		l.PublishedAt = t
	}

	raw, err := json.Marshal(m)
	if err == nil {
		l.Raw = raw
	}
	return l
}

// parseGeo вынимает адрес и координаты из блока geo/геолокации.
func parseGeo(l *Listing, m map[string]any) {
	geo, ok := deepGet(m, "geo")
	if !ok {
		return
	}
	l.Geo = mustJSON(geo)
	l.Address = firstNonEmpty(mapStr(geo, "formattedAddress"), mapStr(geo, "address"), l.Address)
	l.Region = firstNonEmpty(mapStr(geo, "region"), mapStr(geo, "regionName"))
	l.City = firstNonEmpty(mapStr(geo, "city"), mapStr(geo, "cityName"))
	l.District = firstNonEmpty(mapStr(geo, "district"), mapStr(geo, "districtName"))
	l.Metro = firstNonEmpty(mapStr(geo, "metro"), mapStr(geo, "subway"))

	setCoords := func(cm map[string]any) {
		if lat, ok := mapFloat(cm, "lat"); ok && lat != 0 {
			l.Lat = lat
		}
		if lng, ok := mapFloat(cm, "lng"); ok && lng != 0 {
			l.Lng = lng
		} else if lng, ok := mapFloat(cm, "long"); ok && lng != 0 {
			l.Lng = lng
		} else if lng, ok := mapFloat(cm, "lon"); ok && lng != 0 {
			l.Lng = lng
		}
	}
	if cm, ok := deepGet(geo, "coordinates"); ok {
		setCoords(cm)
	} else if cm, ok := deepGet(geo, "map"); ok {
		setCoords(cm)
	} else {
		setCoords(geo)
	}
}

// parseImages собирает изображения, выбирая максимальный размер.
func parseImages(l *Listing, m map[string]any) {
	arr, ok := asArray(mapGet(m, "images"))
	if !ok {
		return
	}
	for _, el := range arr {
		em, ok := asMap(el)
		if !ok {
			continue
		}
		if u := mapStr(em, "url"); u != "" {
			w, _ := mapFloat(em, "width")
			h, _ := mapFloat(em, "height")
			l.Images = append(l.Images, Image{URL: absoluteURL(u), W: int(w), H: int(h)})
			continue
		}
		// формат {«640x480»: {url...}|«url-строка», ...} — размеры в ключе
		best := Image{}
		bestArea := 0
		for key, v := range em {
			var u string
			if sv, isStr := v.(string); isStr {
				u = sv // {"640x480": "https://..."} — url строкой
			} else if im, ok := asMap(v); ok {
				u = mapStr(im, "url")
			}
			if u == "" {
				continue
			}
			w, _ := mapFloat(em, "width")
			h, _ := mapFloat(em, "height")
			if w == 0 || h == 0 {
				if kw, kh, ok := parseSizeKey(key); ok {
					w, h = kw, kh
				}
			}
			area := int(w * h)
			if best.URL == "" || area > bestArea {
				best, bestArea = Image{URL: absoluteURL(u), W: int(w), H: int(h)}, area
			}
		}
		if best.URL != "" {
			l.Images = append(l.Images, best)
		}
	}
	l.ImageCount = len(l.Images)
}

func absoluteURL(path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return baseURL + path
}

// parseSizeKey — "640x480" → (640, 480, true).
func parseSizeKey(key string) (float64, float64, bool) {
	ws, hs, ok := strings.Cut(strings.ToLower(key), "x")
	if !ok {
		return 0, 0, false
	}
	w, err1 := strconv.ParseFloat(strings.TrimSpace(ws), 64)
	h, err2 := strconv.ParseFloat(strings.TrimSpace(hs), 64)
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// findItemData ищет в состояниях карточки объект самого объявления:
// карта, у которой есть params и (description | seller | counters).
func findItemData(states []any) map[string]any {
	for _, st := range states {
		for _, m := range collectMapsWithKey(st, "params") {
			if _, hasDesc := m["description"]; hasDesc {
				return m
			}
			if _, hasSeller := m["seller"]; hasSeller {
				return m
			}
			if _, hasCounters := m["counters"]; hasCounters {
				return m
			}
		}
	}
	// фолбэк: карта с itemId и title
	for _, st := range states {
		for _, m := range collectMapsWithKey(st, "itemId") {
			if mapStr(m, "title") != "" {
				return m
			}
		}
	}
	return nil
}

// ParseItemPage разбирает HTML карточки объявления.
func ParseItemPage(pageHTML string) (*Listing, error) {
	states, err := extractStates(pageHTML)
	if err != nil {
		return nil, err
	}
	data := findItemData(states)
	if data == nil {
		return nil, fmt.Errorf("item data not found in page states")
	}
	return parseItemData(data), nil
}

func parseItemData(m map[string]any) *Listing {
	idStr := firstNonEmpty(mapStr(m, "itemId"), mapStr(m, "id"))
	id, _ := strconv.ParseInt(idStr, 10, 64)

	l := &Listing{ID: id}
	l.Title = mapStr(m, "title")
	l.URLPath = firstNonEmpty(mapStr(m, "urlPath"), mapStr(m, "url"))
	l.URL = absoluteURL(l.URLPath)
	l.Description = firstNonEmpty(mapStr(m, "description"), mapStr(m, "descriptionPreview"))

	if pd, ok := deepGet(m, "priceDetailed"); ok {
		var p priceDetailed
		if b, err := json.Marshal(pd); err == nil && json.Unmarshal(b, &p) == nil {
			l.SetPriceFromDetailed(&p)
			l.PriceMeta = b
		}
	}

	if params, ok := asArray(mapGet(m, "params")); ok {
		var pms []map[string]any
		for _, p := range params {
			if pm, ok := asMap(p); ok {
				pms = append(pms, pm)
			}
		}
		applyParams(l, pms)
		l.Params = mustJSON(pms)
	}

	parseGeo(l, m)
	parseImages(l, m)

	if sm, ok := deepGet(m, "seller"); ok {
		parseSeller(l, sm)
	} else if name := mapStr(m, "sellerName"); name != "" {
		parseSeller(l, m)
	}

	if cm, ok := deepGet(m, "counters"); ok {
		l.Views = mapInt(cm, "views")
		l.Contacts = mapInt(cm, "contacts")
		l.Favorites = mapInt(cm, "favorites")
	}

	l.PublishedAt = firstTime(
		parseEpochMs(mapGet(m, "time")),
		parseEpochMs(mapGet(m, "publishedAt")),
		parseEpochMs(mapGet(m, "creationTime")),
	)
	l.RefreshedAt = firstTime(
		parseEpochMs(mapGet(m, "refreshTime")),
		parseEpochMs(mapGet(m, "refreshedAt")),
		parseEpochMs(mapGet(m, "updateTime")),
	)

	if b, err := json.Marshal(m); err == nil {
		l.Raw = b
	}
	return l
}

func parseSeller(l *Listing, sm map[string]any) {
	si := sellerInfo{
		Name:       firstNonEmpty(mapStr(sm, "sellerName"), mapStr(sm, "name")),
		Type:       mapStr(sm, "sellerType"),
		Company:    firstNonEmpty(mapStr(sm, "companyName"), mapStr(sm, "orgName")),
		URL:        absoluteURL(firstNonEmpty(mapStr(sm, "sellerPath"), mapStr(sm, "url"))),
		Reviews:    int(mapFloatOrZero(sm, "reviewsCount")),
		IsVerified: asBool(mapGet(sm, "isVerified")) || asBool(mapGet(sm, "verified")),
	}
	if r, ok := mapFloat(sm, "rating"); ok {
		si.Rating = r
		l.SellerRating = r
	}
	if si.Name == "" {
		si.Name = l.SellerName
	}
	l.SellerName = firstNonEmpty(si.Name, si.Company)
	if si.Type != "" {
		l.SellerType = sellerKind(si.Type, mapStr(sm, "isCompany"))
	} else if si.Company != "" {
		l.SellerType = "company"
	} else {
		l.SellerType = "private"
	}
	l.SellerURL = si.URL
	l.Seller = mustJSON(si)
}

func mapInt(m map[string]any, key string) int {
	if v, ok := mapFloat(m, key); ok {
		return int(v)
	}
	return 0
}

func mapFloatOrZero(m map[string]any, key string) float64 {
	f, _ := mapFloat(m, key)
	return f
}

func firstTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// SearchRedirect вытаскивает из страницы SSR-редирект Авито (заглушка
// loaderData.redirect / loaderData.data.url вместо выдачи). Возвращает
// путь для перехода или "".
func SearchRedirect(pageHTML string) string {
	states, err := extractStates(pageHTML)
	if err != nil {
		return ""
	}
	for _, st := range states {
		for _, key := range []string{"redirect", "url"} {
			if v, ok := findKey(st, key); ok {
				if s := asString(v); strings.HasPrefix(s, "/") {
					return s
				}
			}
		}
	}
	return ""
}

// ivaStep достаёт payload первого элемента шага из iva-структуры выдачи:
// item.iva.PriceStep[0].payload и т.п.
func ivaStep(m map[string]any, step string) map[string]any {
	iva, ok := deepGet(m, "iva")
	if !ok {
		return nil
	}
	arr, ok := asArray(mapGet(iva, step))
	if !ok || len(arr) == 0 {
		return nil
	}
	first, _ := asMap(arr[0])
	payload, _ := deepGet(first, "payload")
	return payload
}

// applySearchIVA дозаполняет объявление данными реальной выдачи Авито,
// где поля завёрнуты в iva-шаги (PriceStep, UserInfoStep), а комнаты/
// площадь/этаж встречаются только в заголовке.
func applySearchIVA(l *Listing, m map[string]any) {
	if p := ivaStep(m, "PriceStep"); p != nil {
		if l.Price == 0 {
			if pd, ok := deepGet(p, "priceDetailed"); ok {
				var price priceDetailed
				if b, err := json.Marshal(pd); err == nil && json.Unmarshal(b, &price) == nil {
					l.SetPriceFromDetailed(&price)
					l.PriceMeta = b
				}
			}
		}
		if l.PricePerM2 == 0 {
			if s := mapStr(p, "normalizedPrice"); s != "" { // "314 556 ₽ за м²"
				num := leadingInt(s)
				if v, err := strconv.ParseFloat(num, 64); err == nil {
					l.PricePerM2 = int64(v)
					l.PriceUnit = "м2"
				}
			}
		}
	}
	if l.SellerName == "" {
		if u := ivaStep(m, "UserInfoStep"); u != nil {
			if prof, ok := deepGet(u, "profile"); ok {
				l.SellerName = mapStr(prof, "title")
				l.SellerURL = absoluteURL(mapStr(prof, "link"))
				link := mapStr(prof, "link")
				switch {
				case strings.Contains(link, "/brands/"):
					l.SellerType = "developer"
				case strings.Contains(link, "/b/"):
					l.SellerType = "agency"
				default:
					l.SellerType = "private"
				}
				l.Seller = mustJSON(map[string]any{"name": l.SellerName, "url": l.SellerURL, "type": l.SellerType})
			}
		}
	}
	if l.Rooms == 0 && !l.Studio && l.TotalArea == 0 {
		parseTitleParams(l, l.Title)
	}
}

// parseTitleParams извлекает комнаты/площадь/этаж из заголовка выдачи:
// «Квартира-студия, 25,2 м², 3/4 эт.», «1-к. квартира, 41,7 м², 8/9 эт.»
func parseTitleParams(l *Listing, title string) {
	if title == "" {
		return
	}
	// Авито использует неразрывные пробелы (U+00A0/U+202F), которые \s не матчит
	title = strings.NewReplacer("\u00a0", " ", "\u202f", " ").Replace(title)
	if strings.Contains(title, "студия") || strings.Contains(title, "студиЯ") {
		l.Studio = true
	} else if m := regexpRooms.FindStringSubmatch(title); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			l.Rooms = n
		}
	}
	if a := regexpArea.FindStringSubmatch(title); a != nil {
		if v, err := strconv.ParseFloat(strings.ReplaceAll(a[1], ",", "."), 64); err == nil {
			l.TotalArea = v
		}
	}
	if f := regexpFloor.FindStringSubmatch(title); f != nil {
		fl, _ := strconv.Atoi(strings.ReplaceAll(f[1], " ", ""))
		ft, _ := strconv.Atoi(strings.ReplaceAll(f[2], " ", ""))
		l.Floor, l.FloorsTotal = fl, ft
	}
}

var (
	regexpRooms = regexp.MustCompile(`(\d+)[- ]к[. ]`)
	regexpArea  = regexp.MustCompile(`(\d+(?:[.,]\d+)?)\s*м²`)
	regexpFloor = regexp.MustCompile(`(\d+)\s*/\s*(\d+)\s*эт`)
)

// ReparseSearchItem заново разбирает сохранённый raw JSON элемента выдачи
// актуальной версией парсера (офлайн, без сети) — например, после правок схемы.
func ReparseSearchItem(rawJSON []byte, category string, deal DealKind, sourceTask string) (*Listing, error) {
	var m map[string]any
	if err := json.Unmarshal(rawJSON, &m); err != nil {
		return nil, err
	}
	l := parseSearchItem(m)
	if l == nil {
		return nil, fmt.Errorf("cannot parse item")
	}
	l.Category = category
	l.DealType = deal
	l.SourceTask = sourceTask
	return l, nil
}
