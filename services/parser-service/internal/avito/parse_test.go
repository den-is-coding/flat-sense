package avito

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func mustRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func TestExtractStates_MFE(t *testing.T) {
	states, err := extractStates(mustRead(t, "search_mfe.html"))
	if err != nil {
		t.Fatalf("extractStates: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("want 1 state, got %d", len(states))
	}
	if items := findItemsArray(states); len(items) != 2 {
		t.Fatalf("expected items array with 2 entries, got %d", len(items))
	}
}

func TestParseSearchPage(t *testing.T) {
	f := &SearchFilters{City: "moskva", Category: CatFlats, Deal: DealSale}
	listings, err := ParseSearchPage(mustRead(t, "search_mfe.html"), f)
	if err != nil {
		t.Fatalf("ParseSearchPage: %v", err)
	}
	if len(listings) != 2 {
		t.Fatalf("want 2 listings, got %d", len(listings))
	}

	l := listings[0]
	if l.ID != 3221234567 {
		t.Errorf("id = %d", l.ID)
	}
	if l.Title != "2-к. квартира, 54 м², 5/9 эт." {
		t.Errorf("title = %q", l.Title)
	}
	if l.Price != 12500000 || l.Currency != "RUB" {
		t.Errorf("price = %d %s", l.Price, l.Currency)
	}
	if l.PricePerM2 != 231481 || l.PriceUnit != "м2" {
		t.Errorf("price per m2 = %d %s", l.PricePerM2, l.PriceUnit)
	}
	if l.Rooms != 2 {
		t.Errorf("rooms = %d", l.Rooms)
	}
	if l.TotalArea != 54 {
		t.Errorf("total area = %v", l.TotalArea)
	}
	if l.Floor != 5 || l.FloorsTotal != 9 {
		t.Errorf("floor = %d/%d", l.Floor, l.FloorsTotal)
	}
	if l.Address != "Москва, улица Ленина, 10" {
		t.Errorf("address = %q", l.Address)
	}
	if l.Lat == 0 || l.Lng == 0 {
		t.Errorf("coords = %v,%v", l.Lat, l.Lng)
	}
	if l.Category != "kvartiry" || l.DealType != KindSale {
		t.Errorf("category/deal = %s/%s", l.Category, l.DealType)
	}
	if !strings.HasPrefix(l.URL, "https://www.avito.ru/") {
		t.Errorf("url = %q", l.URL)
	}
	if l.ImageCount != 1 || len(l.Images) != 1 {
		t.Errorf("images = %d", l.ImageCount)
	} else if l.Images[0].URL != "https://www.avito.ru/img/111_640.png" || l.Images[0].W != 640 || l.Images[0].H != 480 {
		t.Errorf("image = %+v (ожидался лучший размер 640x480)", l.Images[0])
	}
	if l.PublishedAt.IsZero() {
		t.Errorf("publishedAt пустой")
	}

	studio := listings[1]
	if !studio.Studio || studio.Rooms != 0 {
		t.Errorf("studio flags: rooms=%d studio=%t", studio.Rooms, studio.Studio)
	}
	if studio.TotalArea != 28.5 {
		t.Errorf("studio area = %v", studio.TotalArea)
	}
}

func TestParseSearchPage_Empty(t *testing.T) {
	f := &SearchFilters{City: "moskva", Category: CatFlats, Deal: DealSale}
	listings, err := ParseSearchPage(mustRead(t, "search_empty.html"), f)
	if err != nil {
		t.Fatalf("empty page should parse without error: %v", err)
	}
	if len(listings) != 0 {
		t.Fatalf("want 0 listings, got %d", len(listings))
	}
}

func TestParseItemPage_MFE(t *testing.T) {
	l, err := ParseItemPage(mustRead(t, "item_mfe.html"))
	if err != nil {
		t.Fatalf("ParseItemPage: %v", err)
	}
	if l.ID != 3221234567 {
		t.Errorf("id = %d", l.ID)
	}
	if l.Description != "<p>Просторная квартира с ремонтом. окна во двор.</p>" {
		t.Errorf("description = %q", l.Description)
	}
	if l.LivingArea != 30 || l.KitchenArea != 12 {
		t.Errorf("living/kitchen = %v/%v", l.LivingArea, l.KitchenArea)
	}
	if l.HouseType != "Кирпичный" || l.Renovation != "Евроремонт" {
		t.Errorf("houseType/renovation = %q/%q", l.HouseType, l.Renovation)
	}
	if l.Balcony != "Балкон" || l.Bathroom != "Раздельный" {
		t.Errorf("balcony/bathroom = %q/%q", l.Balcony, l.Bathroom)
	}
	if l.YearBuilt != 1989 {
		t.Errorf("yearBuilt = %d", l.YearBuilt)
	}
	if l.SellerName != "Иван" || l.SellerType != "private" {
		t.Errorf("seller = %q (%q)", l.SellerName, l.SellerType)
	}
	if l.SellerRating != 4.8 {
		t.Errorf("rating = %v", l.SellerRating)
	}
	if l.Views != 1520 || l.Contacts != 34 || l.Favorites != 12 {
		t.Errorf("counters = %d/%d/%d", l.Views, l.Contacts, l.Favorites)
	}
	if l.PublishedAt.IsZero() || l.RefreshedAt.IsZero() {
		t.Errorf("dates: published=%v refreshed=%v", l.PublishedAt, l.RefreshedAt)
	}
	var raw map[string]any
	if err := unmarshalCheck(l.Raw, &raw); err != nil {
		t.Errorf("raw: %v", err)
	}
	var params []map[string]any
	if err := unmarshalCheck(l.Params, &params); err != nil || len(params) != 11 {
		t.Errorf("params: %v len=%d", err, len(params))
	}
}

func TestParseItemPage_InitialData(t *testing.T) {
	l, err := ParseItemPage(mustRead(t, "item_initialdata.html"))
	if err != nil {
		t.Fatalf("ParseItemPage (legacy): %v", err)
	}
	if l.ID != 3221234567 || l.Title != "2-к. квартира, 54 м², 5/9 эт." {
		t.Errorf("legacy parse: id=%d title=%q", l.ID, l.Title)
	}
	if l.Renovation != "Евроремонт" || l.Views != 1520 {
		t.Errorf("legacy fields: renovation=%q views=%d", l.Renovation, l.Views)
	}
}

func TestParseSearchPage_Blocked(t *testing.T) {
	_, err := ParseSearchPage(mustRead(t, "blocked.html"), nil)
	if err == nil {
		t.Fatalf("blocked page must return error")
	}
	if !strings.Contains(err.Error(), "no state scripts") {
		t.Errorf("error = %v", err)
	}
}

func unmarshalCheck(raw []byte, v any) error {
	if len(raw) == 0 {
		return os.ErrInvalid
	}
	return json.Unmarshal(raw, v)
}
