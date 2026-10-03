package avito

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// applyParams разбирает params[] объявления («Имя»: «Значение») и заполняет
// структурные поля Listing. Всё, что не распознано, остаётся в Params (JSONB).
func applyParams(l *Listing, params []map[string]any) {
	for _, p := range params {
		title := mapStr(p, "title")
		if title == "" {
			title = mapStr(p, "name")
		}
		value := mapStr(p, "value")
		if value == "" {
			value = mapStr(p, "text")
		}
		switch title {
		case "Количество комнат":
			if strings.EqualFold(value, "студия") || strings.EqualFold(value, "studio") {
				l.Studio = true
			} else if n, err := strconv.Atoi(leadingInt(value)); err == nil {
				l.Rooms = n
			}
		case "Общая площадь":
			l.TotalArea, _ = parseArea(value)
		case "Жилая площадь":
			l.LivingArea, _ = parseArea(value)
		case "Площадь кухни":
			l.KitchenArea, _ = parseArea(value)
		case "Площадь участка":
			l.LandArea, _ = parseArea(value)
		case "Этаж":
			f, ft := parseFloor(value)
			l.Floor, l.FloorsTotal = f, ft
		case "Этажей в доме":
			if n, err := strconv.Atoi(leadingInt(value)); err == nil {
				l.FloorsTotal = n
			}
		case "Тип дома":
			l.HouseType = value
		case "Ремонт":
			l.Renovation = value
		case "Балкон/Лоджия":
			l.Balcony = value
		case "Санузел":
			l.Bathroom = value
		case "Год постройки", "Год сдачи":
			if n, err := strconv.Atoi(leadingInt(value)); err == nil && n > 1700 && n < 2200 {
				l.YearBuilt = n
			}
		}
		// Название ЖК пишется в params под разными заголовками («ЖК»,
		// «Название ЖК», «Жилой комплекс» и т.п.) — ловим по подстроке.
		if l.ResidentialComplex == "" && containsAnyFold(title, "жк", "жилой комплекс") {
			l.ResidentialComplex = value
		}
	}
}

// containsAnyFold — строка содержит любую из подстрок (без учёта регистра).
func containsAnyFold(s string, subs ...string) bool {
	ls := strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(ls, sub) {
			return true
		}
	}
	return false
}

var reNum = regexp.MustCompile(`[-+]?\d+([.,]\d+)?`)

// leadingInt — первое целое число в строке.
func leadingInt(s string) string {
	m := reNum.FindString(s)
	return strings.ReplaceAll(m, ",", ".")
}

// parseArea — "44,5 м²" / "6 сот." → float.
func parseArea(s string) (float64, bool) {
	m := reNum.FindString(s)
	if m == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", "."), 64)
	return f, err == nil
}

// parseFloor — "5 из 12" → (5, 12).
func parseFloor(s string) (int, int) {
	parts := strings.SplitN(s, "из", 2)
	f, _ := strconv.Atoi(leadingInt(parts[0]))
	if len(parts) == 2 {
		ft, _ := strconv.Atoi(leadingInt(parts[1]))
		return f, ft
	}
	return f, 0
}

// parseEpochMs — время публикации: epoch в секундах или миллисекундах.
func parseEpochMs(v any) time.Time {
	f, ok := asFloat(v)
	if !ok || f <= 0 {
		return time.Time{}
	}
	if f > 1e12 { // миллисекунды
		f /= 1000
	}
	if f > 1e10 { // микросекунды
		f /= 1e6
	}
	return time.Unix(int64(f), 0).UTC()
}

// sellerKindFromStrings приводит тип продавца Авито к нашему перечислению.
func sellerKind(kind, isCompany string) string {
	k := strings.ToLower(kind)
	switch {
	case strings.Contains(k, "dev") || strings.Contains(k, "застройщик"):
		return "developer"
	case strings.Contains(k, "agency") || strings.Contains(k, "агент"):
		return "agency"
	case strings.Contains(k, "company") || strings.Contains(k, "компани") || isCompany == "2":
		return "company"
	default:
		return "private"
	}
}
