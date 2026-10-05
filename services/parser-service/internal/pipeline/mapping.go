package pipeline

import (
	"github.com/yourusername/real-estate-analyzer/parser-service/internal/avito"
	adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"
)

// AdFromListing — avito.Listing → контракт ads.v1.Ad (срез
// avito_listings). Опциональные поля протокола проставляются только
// для ненулевых значений: отсутствие поля означает «не распознано
// парсером», а не ноль (у студий rooms отсутствует, а не равен нулю).
func AdFromListing(l *avito.Listing) *adsv1.Ad {
	ad := &adsv1.Ad{
		Id:                 l.ID,
		Url:                l.URL,
		UrlPath:            l.URLPath,
		Title:              l.Title,
		DealType:           string(l.DealType),
		Category:           l.Category,
		Price:              l.Price,
		Studio:             l.Studio,
		Address:            l.Address,
		City:               l.City,
		District:           l.District,
		Metro:              l.Metro,
		ResidentialComplex: l.ResidentialComplex,
		Description:        l.Description,
	}
	setInt32 := func(dst **int32, v int) {
		if v > 0 {
			x := int32(v)
			*dst = &x
		}
	}
	setFloat64 := func(dst **float64, v float64) {
		if v != 0 {
			x := v
			*dst = &x
		}
	}
	setInt32(&ad.Rooms, l.Rooms)
	setInt32(&ad.Floor, l.Floor)
	setInt32(&ad.FloorsTotal, l.FloorsTotal)
	setFloat64(&ad.TotalArea, l.TotalArea)
	setFloat64(&ad.Lat, l.Lat)
	setFloat64(&ad.Lng, l.Lng)
	for _, img := range l.Images {
		ad.Photos = append(ad.Photos, &adsv1.Photo{
			Url:    img.URL,
			Width:  int32(img.W),
			Height: int32(img.H),
		})
	}
	return ad
}
