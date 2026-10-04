// Package evaluate — оценка конкретного объявления (продажа студии) по
// окупаемости с учётом меблировки (issue #64). Методика сопоставления —
// правила кластеризации из #55 (тот же дом/ЖК, студии отдельно, диапазон
// площади); #55 пока не формализован, выбранные параметры зафиксированы
// в Config и README и вынесены в конфиг, чтобы их можно было уточнить.
package evaluate

import (
	"encoding/json"
	"strings"
)

// Furnishing — признак меблировки объявления.
type Furnishing string

const (
	Furnished   Furnishing = "furnished"   // с мебелью
	Unfurnished Furnishing = "unfurnished" // без мебели
	Unknown     Furnishing = "unknown"     // не удалось определить
)

// Param — параметр объявления «имя: значение» (как на Авито).
type Param struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

// ParamList — список параметров, толерантный к формату дампа: Авито/парсер
// отдаёт [{title,value}], но в выгрузках пустой список бывает объектом {}.
type ParamList []Param

func (p *ParamList) UnmarshalJSON(b []byte) error {
	if string(b) == "{}" || string(b) == "null" {
		*p = nil
		return nil
	}
	var arr []Param
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	*p = arr
	return nil
}

// Geo — минимальный срез geo-объекта объявления: нас интересует
// ссылка на дом (addressLinks.houseLink), по ней строится ключ кластера.
type Geo struct {
	AddressLinks struct {
		HouseLink struct {
			Link string `json:"link"` // /catalog/houses/<city>/<street>/<id>?...
			Text string `json:"text"` // «корп. 2»
		} `json:"houseLink"`
	} `json:"addressLinks"`
}

// Image — фото объявления.
type Image struct {
	URL string `json:"url"`
	W   int    `json:"w,omitempty"`
	H   int    `json:"h,omitempty"`
}

// Listing — объявление (подмножество полей avito_listings; JSON-теги
// совпадают с дампами парсера data/*/listings/*.json и columns БД).
type Listing struct {
	ID          int64     `json:"id"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	DealType    string    `json:"deal_type"` // sale | rent_long | rent_daily
	Category    string    `json:"category"`
	Rooms       *int      `json:"rooms"`
	Studio      bool      `json:"studio"`
	TotalArea   float64   `json:"total_area"`
	Floor       *int      `json:"floor"`
	FloorsTotal *int      `json:"floors_total"`
	Price       int64     `json:"price"`
	Address     string    `json:"address"`
	City        string    `json:"city"`
	District    string    `json:"district"`
	Metro       string    `json:"metro"`
	HouseType   string    `json:"house_type"`
	Description string    `json:"description"`
	Params      ParamList `json:"params"`
	Images      []Image   `json:"images"`
	Geo         *Geo      `json:"geo"`
}

// IsStudio — студия по флагу или по заголовку (в части кампаний флаг не заполнен).
func (l *Listing) IsStudio() bool {
	if l.Studio {
		return true
	}
	return l.Rooms != nil && *l.Rooms == 0 ||
		strings.Contains(strings.ToLower(l.Title), "студия")
}

// HouseKey — ключ сопоставления «тот же дом/ЖК»: id дома из ссылки
// houseLink; если ссылки нет — нормализованный адрес.
func (l *Listing) HouseKey() string {
	if l.Geo != nil {
		if link := l.Geo.AddressLinks.HouseLink.Link; link != "" {
			parts := strings.Split(strings.Split(link, "?")[0], "/")
			if n := len(parts); n > 0 && parts[n-1] != "" {
				return "house:" + parts[n-1]
			}
		}
	}
	return "addr:" + normalizeAddr(l.Address)
}

// normalizeAddr — приведение адреса к сравнимому виду.
func normalizeAddr(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'а' && r <= 'я' || r == 'ё':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ParseListing — разбор объявления из JSON-дампа парсера.
func ParseListing(blob []byte) (*Listing, error) {
	var l Listing
	if err := json.Unmarshal(blob, &l); err != nil {
		return nil, err
	}
	return &l, nil
}
