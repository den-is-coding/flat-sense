package evaluate

import (
	"testing"
)

func TestDetectFurnishing(t *testing.T) {
	cases := []struct {
		name        string
		description string
		params      []Param
		want        Furnishing
		wantCertain bool
	}{
		{"пустое описание — неизвестно", "", nil, Unknown, false},
		{"без мебели", "Сдаётся без мебели и техники.", nil, Unfurnished, true},
		{"мебели нет", "Мебели нет, можно привезти свою.", nil, Unfurnished, true},
		{"не меблирована", "Квартира не меблирована.", nil, Unfurnished, true},
		{"полностью меблирована", "Студия полностью меблирована, кухня встроена.", nil, Furnished, true},
		{"с мебелью", "Сдаю с мебелью и техникой.", nil, Furnished, true},
		{"мебель остаётся", "Вся мебель остаётся покупателю.", nil, Furnished, true},
		{"негатив сильнее позитива", "Мебели нет, мебель можно докупить.", nil, Unfurnished, true},
		{"params: мебель без", "", []Param{{Title: "Мебель", Value: "без"}, {Title: "Кухня", Value: "10"}}, Unfurnished, true},
		{"params: мебель есть", "", []Param{{Title: "Мебель", Value: "есть"}, {Title: "Кухня", Value: "10"}}, Furnished, true},
		{"params: мебель (значение без слова «мебель» в описании)", "тихий двор, парковка", []Param{{Title: "Мебель и техника", Value: "полностью"}}, Furnished, true},
		{"нерелевантная мебель в тексте", "Рядом мебельный магазин.", nil, Unknown, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, certain := DetectFurnishing(c.description, c.params)
			if got != c.want || certain != c.wantCertain {
				t.Fatalf("got (%s, %v), want (%s, %v)", got, certain, c.want, c.wantCertain)
			}
		})
	}
}

// ResolveFurnishing: сомнение консервативно трактуется как «без мебели».
func TestResolveFurnishingConservative(t *testing.T) {
	f, certain := ResolveFurnishing("просто хороший ремонт", nil)
	if f != Unfurnished || certain {
		t.Fatalf("got (%s, %v), want (unfurnished, false)", f, certain)
	}
}

func TestHouseKeyAndStudio(t *testing.T) {
	l := Listing{Address: "Ул. Тамбасова, корп. 2", Title: "Квартира-студия, 21,2 м²"}
	if !l.IsStudio() {
		t.Fatal("студия по заголовку не распознана")
	}
	if l.HouseKey() != "addr:ултамбасовакорп2" {
		t.Fatalf("HouseKey = %q", l.HouseKey())
	}
	l.Geo = new(Geo)
	l.Geo.AddressLinks.HouseLink.Link = "/catalog/houses/sankt-peterburg/ul_tambasova_5/175634?context=xxx"
	if l.HouseKey() != "house:175634" {
		t.Fatalf("HouseKey = %q", l.HouseKey())
	}
}
