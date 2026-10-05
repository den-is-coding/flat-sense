package domain

import (
	"testing"

	adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"
)

// sampleProtoAd — полное объявление контракта (как его публикует
// parser-service в parsed-ads).
func sampleProtoAd() *adsv1.Ad {
	rooms, floor, floors := int32(2), int32(5), int32(9)
	area, lat, lng := 54.3, 55.75, 37.61
	price := int64(12_500_000)
	return &adsv1.Ad{
		Id:                 123456789,
		Url:                "https://www.avito.ru/moskva/kvartiry/2-k_kvartira_123456789",
		UrlPath:            "/moskva/kvartiry/2-k_kvartira_123456789",
		Title:              "2-к квартира, 54 м², 5/9 эт.",
		DealType:           "sale",
		Category:           "kvartiry",
		Price:              price,
		Rooms:              &rooms,
		Studio:             false,
		TotalArea:          &area,
		Floor:              &floor,
		FloorsTotal:        &floors,
		Address:            "Москва, ул. Ленина, 1",
		City:               "Москва",
		District:           "Хамовники",
		Metro:              "Спортивная",
		ResidentialComplex: "Северный парк",
		Lat:                &lat,
		Lng:                &lng,
		Photos: []*adsv1.Photo{
			{Url: "https://img/1.jpg", Width: 1280, Height: 960},
			{Url: "https://img/2.jpg", Width: 640, Height: 480},
		},
		Description: "Светлая квартира, окна во двор",
	}
}

func TestAdFromProtoFull(t *testing.T) {
	p := sampleProtoAd()
	ad, err := AdFromProto(p)
	if err != nil {
		t.Fatalf("AdFromProto: %v", err)
	}
	if ad.AvitoID != 123456789 {
		t.Errorf("AvitoID = %d, хочу 123456789", ad.AvitoID)
	}
	if ad.URL != p.GetUrl() || ad.URLPath != p.GetUrlPath() || ad.Title != p.GetTitle() {
		t.Errorf("строковые поля разошлись: %+v", ad)
	}
	if ad.DealType != "sale" || ad.Category != "kvartiry" {
		t.Errorf("deal_type/category = %s/%s", ad.DealType, ad.Category)
	}
	if ad.Price == nil || *ad.Price != 12_500_000 {
		t.Errorf("Price = %v, хочу 12500000", ad.Price)
	}
	if ad.Rooms == nil || *ad.Rooms != 2 {
		t.Errorf("Rooms = %v, хочу 2", ad.Rooms)
	}
	if ad.TotalArea == nil || *ad.TotalArea != 54.3 {
		t.Errorf("TotalArea = %v, хочу 54.3", ad.TotalArea)
	}
	if ad.Floor == nil || *ad.Floor != 5 || ad.FloorsTotal == nil || *ad.FloorsTotal != 9 {
		t.Errorf("этажи разошлись: %v/%v", ad.Floor, ad.FloorsTotal)
	}
	if ad.Lat == nil || *ad.Lat != 55.75 || ad.Lng == nil || *ad.Lng != 37.61 {
		t.Errorf("координаты разошлись: %v/%v", ad.Lat, ad.Lng)
	}
	if ad.ResidentialComplex != "Северный парк" || ad.Metro != "Спортивная" {
		t.Errorf("ЖК/метро = %s/%s", ad.ResidentialComplex, ad.Metro)
	}
	if len(ad.Photos) != 2 {
		t.Fatalf("Photos = %d шт, хочу 2", len(ad.Photos))
	}
	if ad.Photos[0] != (Photo{URL: "https://img/1.jpg", Width: 1280, Height: 960}) {
		t.Errorf("Photos[0] = %+v", ad.Photos[0])
	}
}

// Ноль протокола = «не распознано» → NULL (nil), а не ноль в БД.
func TestAdFromProtoZeroMeansAbsent(t *testing.T) {
	ad, err := AdFromProto(&adsv1.Ad{Id: 42, Url: "https://www.avito.ru/x", Studio: true})
	if err != nil {
		t.Fatalf("AdFromProto: %v", err)
	}
	if ad.Price != nil || ad.Rooms != nil || ad.TotalArea != nil ||
		ad.Floor != nil || ad.FloorsTotal != nil || ad.Lat != nil || ad.Lng != nil {
		t.Errorf("опциональные поля должны быть nil: %+v", ad)
	}
	if !ad.Studio {
		t.Error("Studio = false, хочу true")
	}
	if len(ad.Photos) != 0 {
		t.Errorf("Photos = %d, хочу 0", len(ad.Photos))
	}
}

func TestAdFromProtoErrors(t *testing.T) {
	if _, err := AdFromProto(nil); err == nil {
		t.Error("nil объявление должно давать ошибку")
	}
	if _, err := AdFromProto(&adsv1.Ad{Url: "https://www.avito.ru/x"}); err == nil {
		t.Error("объявление без id должно давать ошибку")
	}
}

// Полный круг домена: Ad → proto → Ad — без потерь (кроме служебных
// CreatedAt/UpdatedAt/RequestID, которых в контракте нет).
func TestAdToProtoRoundTrip(t *testing.T) {
	src, err := AdFromProto(sampleProtoAd())
	if err != nil {
		t.Fatalf("AdFromProto: %v", err)
	}
	back, err := AdFromProto(src.ToProto())
	if err != nil {
		t.Fatalf("AdFromProto(ToProto): %v", err)
	}
	if src.AvitoID != back.AvitoID || src.Title != back.Title || src.Price == nil || back.Price == nil || *src.Price != *back.Price {
		t.Errorf("round trip потерял данные: %+v vs %+v", src, back)
	}
	if len(src.Photos) != len(back.Photos) || src.Photos[0] != back.Photos[0] {
		t.Errorf("round trip потерял фото: %+v vs %+v", src.Photos, back.Photos)
	}
	if back.Rooms == nil || *back.Rooms != 2 || back.TotalArea == nil || *back.TotalArea != 54.3 {
		t.Errorf("round trip потерял опциональные поля: %+v", back)
	}
}
