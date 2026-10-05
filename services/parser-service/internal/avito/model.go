package avito

import (
	"encoding/json"
	"time"
)

// DealKind — нормализованный тип сделки (пишется в БД).
type DealKind string

const (
	KindSale      DealKind = "sale"
	KindRentLong  DealKind = "rent_long"
	KindRentDaily DealKind = "rent_daily"
)

// Param — параметр объявления в виде «имя: значение» (как на Авито).
type Param struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

// Image — изображение объявления (лучший доступный размер).
type Image struct {
	URL string `json:"url"`
	W   int    `json:"w,omitempty"`
	H   int    `json:"h,omitempty"`
}

// Listing — объявление недвижимости, готовое к записи в PostgreSQL.
type Listing struct {
	ID       int64 // avito item id (PK)
	URL      string
	URLPath  string
	Category string
	DealType DealKind

	Title       string
	Description string

	Price      int64
	Currency   string
	PricePerM2 int64
	PriceUnit  string
	PriceMeta  json.RawMessage

	// Ключевые параметры недвижимости (распарсены из params)
	Rooms       int
	Studio      bool
	TotalArea   float64 // м²
	LivingArea  float64
	KitchenArea float64
	LandArea    float64 // сотки
	Floor       int
	FloorsTotal int
	HouseType   string
	Renovation  string
	Balcony     string
	Bathroom    string
	YearBuilt   int

	// География
	Address            string
	Region             string
	City               string
	ResidentialComplex string
	District           string
	Metro              string
	Lat                float64
	Lng                float64
	Geo                json.RawMessage

	// Продавец
	SellerName   string
	SellerType   string // private | agency | developer | company
	SellerURL    string
	SellerRating float64
	Seller       json.RawMessage

	// Медиа
	Images     []Image
	ImageCount int

	// Метрики
	Views     int
	Contacts  int
	Favorites int

	PublishedAt time.Time
	RefreshedAt time.Time

	Params     json.RawMessage
	Raw        json.RawMessage
	SourceTask string

	// заполняется репозиторием
	IsNew bool
}

// priceDetailed из JSON Авито.
type priceDetailed struct {
	Value        float64 `json:"value"`
	Currency     string  `json:"currency"`
	PricePerPart struct {
		Value float64 `json:"value"`
		Title string  `json:"title"` // "за м²" / "за сотку"
		Unit  string  `json:"unit"`  // "м2" / "сотка"
	} `json:"pricePerPart"`
	Title        string `json:"title"`
	IsUserReview bool   `json:"isUserReview,omitempty"`
}

// SetPriceFromDetailed заполняет ценовые поля из priceDetailed.
func (l *Listing) SetPriceFromDetailed(pd *priceDetailed) {
	if pd == nil {
		return
	}
	l.Price = int64(pd.Value)
	l.Currency = pd.Currency
	if pd.Currency == "" {
		l.Currency = "RUB"
	}
	if pd.PricePerPart.Value > 0 {
		l.PricePerM2 = int64(pd.PricePerPart.Value)
		l.PriceUnit = pd.PricePerPart.Unit
		if l.PriceUnit == "" {
			l.PriceUnit = pd.PricePerPart.Title
		}
	}
}

// SellerInfo — агрегированная информация о продавце.
type sellerInfo struct {
	Name       string  `json:"name,omitempty"`
	Type       string  `json:"type,omitempty"` // private | company | developer
	Company    string  `json:"company,omitempty"`
	URL        string  `json:"url,omitempty"`
	Rating     float64 `json:"rating,omitempty"`
	Reviews    int     `json:"reviews,omitempty"`
	Phone      string  `json:"phone,omitempty"`
	IsVerified bool    `json:"isVerified,omitempty"`
}
