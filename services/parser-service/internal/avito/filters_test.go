package avito

import (
	"net/url"
	"testing"
)

func TestBuildURL_Sale(t *testing.T) {
	f := &SearchFilters{
		City: "moskva", Category: CatFlats, Deal: DealSale,
		PriceMin: 5000000, PriceMax: 15000000,
		Rooms:      []int{2, 3},
		SellerType: SellerPrivate,
		Sort:       SortCheaper,
		Advanced:   []string{"f=ASgBAgICA0TQgPl4xmNOTm90fGZsYXRz"},
	}
	u, err := url.Parse(f.BuildURL(2))
	if err != nil {
		t.Fatalf("build url: %v", err)
	}
	if u.Path != "/moskva/kvartiry/prodam" {
		t.Errorf("path = %s", u.Path)
	}
	q := u.Query()
	if q.Get("pmin") != "5000000" || q.Get("pmax") != "15000000" {
		t.Errorf("price params: %v", q)
	}
	if q.Get("s") != "104" {
		t.Errorf("sort = %s", q.Get("s"))
	}
	if q.Get("user") != "1" {
		t.Errorf("user = %s", q.Get("user"))
	}
	if q.Get("p") != "2" {
		t.Errorf("page = %s", q.Get("p"))
	}
	if !jsonValid(q.Get("f")) || q.Get("f") != "ASgBAgICA0TQgPl4xmNOTm90fGZsYXRz" {
		t.Errorf("f = %s", q.Get("f"))
	}
}

func TestBuildURL_RentLongFlats(t *testing.T) {
	f := &SearchFilters{City: "sankt-peterburg", Category: CatFlats, Deal: DealRentLong}
	u, _ := url.Parse(f.BuildURL(1))
	if u.Path != "/sankt-peterburg/kvartiry/sdam_na_dlitelnyy_srok" {
		t.Errorf("path = %s", u.Path)
	}
}

func TestBuildURL_RentDaily(t *testing.T) {
	f := &SearchFilters{City: "moskva", Category: CatRooms, Deal: DealRentDaily}
	u, _ := url.Parse(f.BuildURL(1))
	if u.Path != "/moskva/komnaty/sdam_posutochno" {
		t.Errorf("path = %s", u.Path)
	}
}

func TestBuildURL_RentNonFlat(t *testing.T) {
	// у домов нет отдельного «на длительный срок» — общий sdam
	f := &SearchFilters{City: "moskva", Category: CatHouses, Deal: DealRentLong}
	u, _ := url.Parse(f.BuildURL(1))
	if u.Path != "/moskva/doma_dachi_kottedzhi/sdam" {
		t.Errorf("path = %s", u.Path)
	}
}

func TestFiltersFromURL(t *testing.T) {
	raw := "https://www.avito.ru/moskva/kvartiry/sdam_na_dlitelnyy_srok?" +
		"pmin=30000&pmax=80000&f=ASgBAgICA0SUA~AQ4LNcBQFI6ZmF0dXJl&s=104&user=2&p=3&send_timestamp=123"
	f, err := FiltersFromURL(raw)
	if err != nil {
		t.Fatalf("FiltersFromURL: %v", err)
	}
	if f.City != "moskva" || f.Category != CatFlats || f.Deal != DealRentLong {
		t.Errorf("base: %s/%s/%s", f.City, f.Category, f.Deal)
	}
	if f.PriceMin != 30000 || f.PriceMax != 80000 || f.Page != 3 {
		t.Errorf("params: %d-%d p%d", f.PriceMin, f.PriceMax, f.Page)
	}
	if f.Sort != SortCheaper || f.SellerType != SellerCompany {
		t.Errorf("sort/user: %s %s", f.Sort, f.SellerType)
	}
	if len(f.Advanced) != 1 || f.Advanced[0] != "ASgBAgICA0SUA~AQ4LNcBQFI6ZmF0dXJl" {
		t.Errorf("advanced = %v", f.Advanced)
	}
	// неизвестный параметр сохраняется и воспроизводится при построении URL
	u, _ := url.Parse(f.BuildURL(3))
	if u.Query().Get("send_timestamp") != "123" {
		t.Errorf("extra param lost: %v", u.RawQuery)
	}
	if u.Query().Get("f") == "" {
		t.Errorf("f lost")
	}
}

func TestFiltersFromURL_CategoryOnly(t *testing.T) {
	f, err := FiltersFromURL("https://www.avito.ru/moskva/kvartiry")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if f.Deal != DealSale {
		t.Errorf("deal = %s", f.Deal)
	}
	if _, err := FiltersFromURL("https://example.com/x"); err == nil {
		t.Errorf("не-авито URL должен давать ошибку")
	}
}

func TestJoinFSlobs(t *testing.T) {
	got := joinFSlobs([]string{"f=AAA.BBB", "CCC", "f="})
	if got != "AAA.BBB.CCC" {
		t.Errorf("join = %q", got)
	}
}

func TestValidate(t *testing.T) {
	if err := (&SearchFilters{}).Validate(); err == nil {
		t.Errorf("empty filters must fail validation")
	}
	if err := (&SearchFilters{City: "moskva", Category: CatFlats, Deal: DealSale}).Validate(); err != nil {
		t.Errorf("valid filters rejected: %v", err)
	}
}

func jsonValid(s string) bool { return s != "" }
