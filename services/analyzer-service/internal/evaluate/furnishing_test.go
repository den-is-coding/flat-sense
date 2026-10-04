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

// Предметный признак: перечисление мебели/техники → furnished.
func TestDetectFurnishingItems(t *testing.T) {
	f, ok, ev := DetectFurnishingDetailed("Новая студия: двуспальная кровать, шкаф-купе, телевизор на стене.", nil)
	if f != Furnished || !ok {
		t.Fatalf("got (%s, %v), want furnished/true", f, ok)
	}
	if ev == "" || !contains(ev, "кровать") {
		t.Fatalf("evidence должен содержать триггеры: %q", ev)
	}
	// Один предмет — недостаточно («кухня» может быть комнатой).
	if f, ok, _ = DetectFurnishingDetailed("Студия с кухней-гостиной, тёплый пол.", nil); f != Unknown || ok {
		t.Fatalf("один предмет: got (%s, %v), want unknown/false", f, ok)
	}
}

// «Обставите под себя» и синонимы → unfurnished.
func TestDetectFurnishingSelfFurnish(t *testing.T) {
	cases := []string{
		"Студия сдаётся пустой — сможете обставить под себя.",
		"Ремонт свежий, обустроите по своему вкусу.",
		"Заезжай со своей мебелью и техникой.",
	}
	for _, d := range cases {
		if f, ok, _ := DetectFurnishingDetailed(d, nil); f != Unfurnished || !ok {
			t.Errorf("%q: got (%s, %v), want unfurnished/true", d, f, ok)
		}
	}
	// Явный «без мебели» всё ещё сильнее и идёт первым.
	if f, _, ev := DetectFurnishingDetailed("Без мебели, обставите по своему вкусу.", nil); f != Unfurnished || contains(ev, "обставить") {
		t.Errorf("негатив должен срабатывать первым: %s / %q", f, ev)
	}
}

// Sale-вход: предметный признак применяется (500к только где мебели нет).
func TestResolveFurnishingSaleItems(t *testing.T) {
	// Меблированная продажа: перечисление предметов → furnished, надбавки не будет.
	f, ok := ResolveFurnishing("Продам студию: остаётся двуспальная кровать, шкаф-купе, диван и телевизор.", nil)
	if f != Furnished || !ok {
		t.Fatalf("got (%s, %v), want furnished/true", f, ok)
	}
	// Только техника+кухня без спальной мебели — не «меблирована»:
	// консервативно «без мебели» (надбавка 500к применяется).
	f, ok = ResolveFurnishing("Остаётся кухонный гарнитур: посудомойка, холодильник, стиральная машина.", nil)
	if f != Unfurnished || ok {
		t.Fatalf("got (%s, %v), want unfurnished/false (только техника)", f, ok)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Омоглифы не ломают предметный признак.
func TestDetectFurnishingItemsHomoglyphs(t *testing.T) {
	f, ok, _ := DetectFurnishingDetailed("cдaeтся c двуx яpycной кpoвaтью и шкaфoм, вce ocтaeтcя", nil)
	if f != Furnished || !ok {
		t.Fatalf("got (%s, %v), want furnished/true", f, ok)
	}
}
