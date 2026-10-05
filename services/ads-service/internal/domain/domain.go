// Package domain — ядро ads-service (issue #5): модель объявления
// (проекция avito_listings), результат анализа и порт хранилища.
// Адаптеры (postgres, kafka-консьюмер, gRPC) зависят от этого пакета,
// ядро от адаптеров — не зависит.
package domain

import (
	"context"
	"errors"
	"time"

	adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"
)

// ErrNotFound — объявление (или его анализ) отсутствует в проекции.
// Адаптеры транслируют его в gRPC codes.NotFound.
var ErrNotFound = errors.New("ads: не найдено")

// Photo — фотография объявления (элемент photos в БД и ads.v1.Photo).
type Photo struct {
	URL    string `json:"url"`
	Width  int32  `json:"width"`
	Height int32  `json:"height"`
}

// Ad — проекция объявления для чтения (таблица ads, миграция 000018).
// Опциональные поля (цена, комнаты, площадь, этаж, координаты) — nil,
// когда парсер их не распознал: в БД NULL, в контракте — отсутствие.
type Ad struct {
	AvitoID            int64
	URL                string
	URLPath            string
	Title              string
	DealType           string
	Category           string
	Price              *int64
	PriceCurrency      string
	Rooms              *int32
	Studio             bool
	TotalArea          *float64
	Floor              *int32
	FloorsTotal        *int32
	Address            string
	City               string
	District           string
	Metro              string
	ResidentialComplex string
	Lat                *float64
	Lng                *float64
	Photos             []Photo
	Description        string
	RequestID          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Analysis — результат анализа объявления (таблица analysis_results):
// минимальный срез analyzer.v1.AnalysisResult для карточки объекта.
// RentForecast/YieldPercent/PaybackYears — nil, если арендных данных нет.
type Analysis struct {
	AdID         int64
	Status       string
	RentForecast *float64
	YieldPercent *float64
	PaybackYears *float64
	Payload      []byte // analyzer.v1.AnalysisResult целиком (protojson)
	ComputedAt   time.Time
}

// AdRepository — порт хранилища проекции (реализует *postgres.Store;
// в тестах адаптеров подменяется фейком).
type AdRepository interface {
	// UpsertAd — создать или обновить объявление по AvitoID.
	// created=true — строка вставлена, false — обновлена существующая.
	UpsertAd(ctx context.Context, ad Ad) (created bool, err error)
	// GetAd — объявление по avito_id; ErrNotFound, если строки нет.
	GetAd(ctx context.Context, avitoID int64) (Ad, error)
	// SimilarAds — похожие объявления (эвристика, см. postgres.Store).
	SimilarAds(ctx context.Context, ad Ad, limit int) ([]Ad, error)
	// SaveAnalysis — создать/перезаписать результат анализа по AdID.
	SaveAnalysis(ctx context.Context, a Analysis) error
}

// AdFromProto — контракт ads.v1.Ad → доменная модель. Ошибка — когда
// объявления нет или у него нет id (upsert по avito_id невозможен);
// консьюмер вернёт её наружу и сообщение уйдёт в DLQ: такие события
// повторами не лечатся.
func AdFromProto(p *adsv1.Ad) (Ad, error) {
	if p == nil {
		return Ad{}, errors.New("ads: пустое объявление в событии")
	}
	if p.GetId() == 0 {
		return Ad{URL: p.GetUrl()}, errors.New("ads: объявление без id (avito_id)")
	}
	ad := Ad{
		AvitoID:            p.GetId(),
		URL:                p.GetUrl(),
		URLPath:            p.GetUrlPath(),
		Title:              p.GetTitle(),
		DealType:           p.GetDealType(),
		Category:           p.GetCategory(),
		PriceCurrency:      "RUB",
		Studio:             p.GetStudio(),
		Address:            p.GetAddress(),
		City:               p.GetCity(),
		District:           p.GetDistrict(),
		Metro:              p.GetMetro(),
		ResidentialComplex: p.GetResidentialComplex(),
		Description:        p.GetDescription(),
	}
	// proto3-скаляр 0 = «не распознано» → NULL в проекции.
	if p.GetPrice() != 0 {
		v := p.GetPrice()
		ad.Price = &v
	}
	if p.Rooms != nil {
		v := p.GetRooms()
		ad.Rooms = &v
	}
	if p.TotalArea != nil {
		v := p.GetTotalArea()
		ad.TotalArea = &v
	}
	if p.Floor != nil {
		v := p.GetFloor()
		ad.Floor = &v
	}
	if p.FloorsTotal != nil {
		v := p.GetFloorsTotal()
		ad.FloorsTotal = &v
	}
	if p.Lat != nil {
		v := p.GetLat()
		ad.Lat = &v
	}
	if p.Lng != nil {
		v := p.GetLng()
		ad.Lng = &v
	}
	for _, ph := range p.GetPhotos() {
		if ph == nil {
			continue
		}
		ad.Photos = append(ad.Photos, Photo{URL: ph.GetUrl(), Width: ph.GetWidth(), Height: ph.GetHeight()})
	}
	return ad, nil
}

// ToProto — доменная модель → контракт ads.v1.Ad (GetAd/GetSimilarAds).
func (a Ad) ToProto() *adsv1.Ad {
	p := &adsv1.Ad{
		Id:                 a.AvitoID,
		Url:                a.URL,
		UrlPath:            a.URLPath,
		Title:              a.Title,
		DealType:           a.DealType,
		Category:           a.Category,
		Studio:             a.Studio,
		Address:            a.Address,
		City:               a.City,
		District:           a.District,
		Metro:              a.Metro,
		ResidentialComplex: a.ResidentialComplex,
		Description:        a.Description,
	}
	if a.Price != nil {
		p.Price = *a.Price
	}
	if a.Rooms != nil {
		v := *a.Rooms
		p.Rooms = &v
	}
	if a.TotalArea != nil {
		v := *a.TotalArea
		p.TotalArea = &v
	}
	if a.Floor != nil {
		v := *a.Floor
		p.Floor = &v
	}
	if a.FloorsTotal != nil {
		v := *a.FloorsTotal
		p.FloorsTotal = &v
	}
	if a.Lat != nil {
		v := *a.Lat
		p.Lat = &v
	}
	if a.Lng != nil {
		v := *a.Lng
		p.Lng = &v
	}
	for _, ph := range a.Photos {
		p.Photos = append(p.Photos, &adsv1.Photo{Url: ph.URL, Width: ph.Width, Height: ph.Height})
	}
	return p
}
