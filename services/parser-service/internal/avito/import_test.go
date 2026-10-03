package avito

import (
	"strings"
	"testing"
)

// Импорт дампа кампании (драйвер ручного сбора) в структуру Listing.
func TestImportListings(t *testing.T) {
	dump := `{
	  "meta": {"query": "Студии, СПб, 7.5-7.55M"},
	  "items": [
	    {
	      "avito_id": 8142691466,
	      "url": "/sankt-peterburg/kvartiry/kvartira-studiya_277_m_14_et._8142691466",
	      "title": "Квартира-студия, 27,7\u00a0м², 1/4\u00a0эт.",
	      "price": 7543818,
	      "address": "пос. Шушары, уч. 22, стр. 1",
	      "coordinates": {"lat": 59.75963, "lng": 30.32891},
	      "metro": [{"station": "р-н Пушкинский"}],
	      "house_params": {
	        "Название новостройки": "ЖК «Пулково Лейк»",
	        "Тип дома": "панельный",
	        "Срок сдачи": "Сдача в 4 кв. 2026",
	        "Этажей в доме": "4"
	      },
	      "description": "Продается студия в ЖК «Пулково Лейк»",
	      "image_urls_1280": ["https://img.avito.st/1.jpg", "https://img.avito.st/2.jpg"],
	      "images_count": 16,
	      "developer": "Группа компаний «ЛСР»",
	      "seller_is_developer": true
	    },
	    {
	      "avito_id": 8363009965,
	      "url": "/sankt-peterburg/kvartiry/kvartira-studiya_214_m_29_et._8363009965",
	      "title": "Квартира-студия, 21,4\u00a0м², 2/9\u00a0эт.",
	      "price": 7508124,
	      "address": "пр-т Мечникова, д. 40, лит. А",
	      "metro": [{"station": "Площадь Мужества"}, {"station": "Политехническая"}],
	      "images_count": 28
	    },
	    {"avito_id": 0, "title": "битая карточка"}
	  ]
	}`
	listings, err := ImportListings([]byte(dump), "test-campaign")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(listings) != 2 {
		t.Fatalf("listings = %d, want 2 (битая карточка пропущена)", len(listings))
	}
	l := listings[0]
	if l.ID != 8142691466 || l.Price != 7543818 {
		t.Fatalf("id/price: %d/%d", l.ID, l.Price)
	}
	if !l.Studio {
		t.Fatal("studio must be detected from title")
	}
	if l.TotalArea < 27.69 || l.TotalArea > 27.71 {
		t.Fatalf("total area = %v", l.TotalArea)
	}
	if l.Floor != 1 || l.FloorsTotal != 4 {
		t.Fatalf("floor = %d/%d", l.Floor, l.FloorsTotal)
	}
	// цена за м² вычислена
	if l.PricePerM2 == 0 {
		t.Fatal("price per m2 not computed")
	}
	if l.ResidentialComplex != "ЖК «Пулково Лейк»" {
		t.Fatalf("complex = %q", l.ResidentialComplex)
	}
	if l.HouseType != "панельный" || l.YearBuilt != 2026 {
		t.Fatalf("house = %q year = %d", l.HouseType, l.YearBuilt)
	}
	if l.City != "Санкт-Петербург" {
		t.Fatalf("city = %q", l.City)
	}
	if l.District != "р-н Пушкинский" {
		t.Fatalf("district = %q", l.District)
	}
	if l.Lat != 59.75963 || l.Lng != 30.32891 {
		t.Fatalf("coords = %v/%v", l.Lat, l.Lng)
	}
	if l.SellerType != "developer" || l.SellerName != "Группа компаний «ЛСР»" {
		t.Fatalf("seller = %q/%q", l.SellerType, l.SellerName)
	}
	if l.ImageCount != 16 || len(l.Images) != 2 {
		t.Fatalf("images = %d/%d", l.ImageCount, len(l.Images))
	}
	if l.SourceTask != "test-campaign" {
		t.Fatalf("source task = %q", l.SourceTask)
	}
	if len(l.Raw) == 0 || !strings.Contains(string(l.Raw), "8142691466") {
		t.Fatal("raw json must be preserved")
	}
	// вторая карточка: метро без района, без координат, без house_params
	l2 := listings[1]
	if l2.Metro != "Площадь Мужества, Политехническая" || l2.District != "" {
		t.Fatalf("metro/district = %q/%q", l2.Metro, l2.District)
	}
	if l2.Lat != 0 || l2.Lng != 0 {
		t.Fatal("no coordinates in source, must not be set")
	}
}
