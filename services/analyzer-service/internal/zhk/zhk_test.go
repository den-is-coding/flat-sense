package zhk

import (
	"testing"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

func TestMatchByAddress(t *testing.T) {
	cases := []struct {
		addr string
		want string // каноничное имя, "" — не распознан
	}{
		{"Санкт-Петербург, Парашютная ул., 42к1", "Граффити"},
		{"Парашютная ул., 42к2", "Граффити"}, // без города
		{"Санкт-Петербург, Парашютная ул., 71к2", "Прайм Приморский"},
		{"Санкт-Петербург, Архивная ул., 4", "Пульс Премьер"},
		{"Санкт-Петербург, Кубинская ул., 82к3с1", "CUBE"},
		{"ул. Кубинская, стр. 1", "Титул"},
		{"Санкт-Петербург, Зверинская ул., 2/5", ""},
		{"", ""},
	}
	for _, c := range cases {
		l := &evaluate.Listing{Address: c.addr}
		got := Match(l)
		name := ""
		if got != nil {
			name = got.Name
		}
		if name != c.want {
			t.Errorf("Match(%q) = %q, want %q", c.addr, name, c.want)
		}
	}
}

func TestMatchByName(t *testing.T) {
	cases := []struct {
		label string
		desc  string
		want  string
	}{
		// имя ЖК в бейдже/описании, адрес — не из реестра
		{"ЖК «Пульс Премьер»", "", "Пульс Премьер"},
		{"", `Сдаётся студия в ЖK "Грaффити"`, "Граффити"}, // латинские двойники
		{"", "новый ЖК Титул в Московском, сдача 2027", "Титул"},
		{"ЖК «Сенат»", "", "Сенат"},
		// «титульное страхование» — не ЖК Титул
		{"", "расходы на титульное страхование сделки", ""},
		// «прайм» без уточнения — не Прайм Приморский
		{"ЖК «Прайм»", "", ""},
	}
	for _, c := range cases {
		l := &evaluate.Listing{
			Address:             "Санкт-Петербург, Лиговский пр., 50",
			ResidentialComplex:  c.label,
			Description:         c.desc,
		}
		got := Match(l)
		name := ""
		if got != nil {
			name = got.Name
		}
		if name != c.want {
			t.Errorf("Match(label=%q desc=%q) = %q, want %q", c.label, c.desc, name, c.want)
		}
	}
}

func TestEnrichAliases(t *testing.T) {
	// Аренда в корпусе 42к1 получает адреса всех корпусов Граффити —
	// студия продажи в 42к2 кластеризуется с ней на уровне ЖК.
	rent := &evaluate.Listing{
		DealType: "rent_long",
		Address:  "Санкт-Петербург, Парашютная ул., 42к1",
	}
	if c := Enrich(rent); c == nil || c.Name != "Граффити" {
		t.Fatalf("Enrich rent: %v, want Граффити", c)
	}
	if len(rent.Aliases) == 0 {
		t.Fatal("Enrich: aliases not set")
	}

	sale := &evaluate.Listing{
		DealType: "sale",
		Address:  "Парашютная ул., 42к2",
	}
	saleKeys := map[string]bool{}
	for _, k := range sale.Keys() {
		saleKeys[k] = true
	}
	hit := false
	for _, k := range rent.Keys() {
		if saleKeys[k] {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("no key intersection between 42к1 rent %v and 42к2 sale %v", rent.Keys(), sale.Keys())
	}

	// Неизвестный ЖК — алиасы не трогаем.
	other := &evaluate.Listing{Address: "Лиговский пр., 50"}
	if c := Enrich(other); c != nil || len(other.Aliases) != 0 {
		t.Fatalf("Enrich unknown: %v aliases=%v, want nil/empty", c, other.Aliases)
	}
}
