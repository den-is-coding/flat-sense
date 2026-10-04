package evaluate

import "strings"

// Определение мебели по тексту объявления (issue #64):
// признаки аренды/продажи (Avito отдаёт часть признаков в params)
// + фолбэк-эвристика по описанию. Правила зафиксированы юнит-тестами
// (furnishing_test.go); сомнение трактуется консервативно — «без мебели».

// Слова-маркеры. Порядок важен: негатив («без мебели») сильнее позитива.
var (
	// «без мебели», «мебели нет», «не меблирована», «без техники и мебели»
	unfurnishedPhrases = []string{
		"без мебели", "без мебелью", "мебели нет", "мебели отсутствуют",
		"не меблирован", "не меблирова", "нет мебели", "пустая, без",
	}
	// «полностью меблирована», «с мебелью», «мебель вся остаётся» и т.п.
	furnishedPhrases = []string{
		"меблирован", "с мебелью", "со всей мебелью", "мебель вся",
		"вся мебель", "мебель остаётся", "мебель остается", "мебель полностью",
		"есть вся необходимая мебель", "полностью укомплектована мебелью",
	}
)

// DetectFurnishing определяет признак меблировки по описанию и параметрам.
// Возвращает (признак, определён_ли_уверенно).
func DetectFurnishing(description string, params []Param) (Furnishing, bool) {
	// 1) Явный признак в params (Авито: «Мебель и техника», «Мебель»).
	for _, p := range params {
		title := strings.ToLower(p.Title)
		if !strings.Contains(title, "мебел") {
			continue
		}
		v := strings.ToLower(p.Value)
		switch {
		case strings.Contains(v, "без"):
			return Unfurnished, true
		case v != "" && v != "нет":
			return Furnished, true
		}
	}
	// 2) Эвристика по описанию: сначала негативные маркеры (консервативно),
	// затем позитивные.
	text := strings.ToLower(description)
	if hasAny(text, unfurnishedPhrases) {
		return Unfurnished, true
	}
	if hasAny(text, furnishedPhrases) {
		return Furnished, true
	}
	return Unknown, false
}

// ResolveFurnishing — меблировка входного объявления с консервативным
// правилом из issue: сомнение = «без мебели» (помечается в отчёте).
func ResolveFurnishing(description string, params []Param) (Furnishing, bool) {
	f, ok := DetectFurnishing(description, params)
	if !ok {
		return Unfurnished, false // консервативное допущение
	}
	return f, true
}

func hasAny(text string, phrases []string) bool {
	for _, p := range phrases {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}
