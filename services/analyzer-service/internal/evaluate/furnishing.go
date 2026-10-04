package evaluate

import "strings"

// Определение мебели по тексту объявления (issue #64):
// признаки аренды/продажи (Avito отдаёт часть признаков в params)
// + фолбэк-эвристика по описанию. Правила зафиксированы юнит-тестами
// (furnishing_test.go); сомнение трактуется консервативно — «без мебели».
//
// Порядок правил: явный негатив → маркеры «обставить под себя» → явный
// позитив → перечисление предметов мебели (≥2 разных) → неизвестно.

// Слова-маркеры. Порядок важен: негатив («без мебели») сильнее позитива.
var (
	// «без мебели», «мебели нет», «не меблирована», «без техники и мебели»
	unfurnishedPhrases = []string{
		"без мебели", "без мебелью", "мебели нет", "мебели отсутствуют",
		"не меблирован", "не меблирова", "нет мебели", "пустая, без",
	}
	// «сможете обставить под себя», «по своему вкусу», «со своей мебелью» —
	// квартира сдаётся пустой, мебель tenants привозят сами.
	noFurniturePhrases = []string{
		"обставить", "по своему вкусу", "по вашему вкусу",
		"со своей мебелью", "своей мебелью", "со своей техникой",
		"своей техникой", "под свои", "со своей бытовой техникой",
	}
	// «полностью меблирована», «с мебелью», «мебель вся остаётся» и т.п.
	furnishedPhrases = []string{
		"меблирован", "с мебелью", "со всей мебелью", "мебель вся",
		"вся мебель", "мебель остаётся", "мебель остается", "мебель полностью",
		"есть вся необходимая мебель", "полностью укомплектована мебелью",
		"мебель и техника", "мебелью и техникой", "техника и мебель",
		"укомплектован",
	}
	// Предметы мебели/техники: перечисление конкретики — сильный признак
	// меблированной сдачи («кровать, шкаф, телевизор…»).
	furnitureItems = []string{
		"кровать", "диван", "стол", "шкаф", "кухня", "кухонн",
		"посудомо", "телевизор", "хранения", "комод", "тумба",
		"холодильник", "стиральная машина", "стирал", "духовка",
		"матрас", "тумбочка", "вешалк", "зеркало", "микроволнов",
	}
	// furnitureProper — «настоящая» мебель (сон/хранение/гостиная): бытовая
	// техника и кухня сами по себе квартиру меблированной не делают,
	// поэтому среди упомянутых предметов должен быть хотя бы один такой.
	furnitureProper = []string{
		"кровать", "диван", "шкаф", "комод", "матрас",
		"тумбочка", "тумба", "вешалк", "гардероб", "кресло",
	}
)

// homoglyphs — латинские двойники кириллицы (продавцы маскируют текст,
// «сдaeтся», «мeбель») — приводим к кириллице перед поиском фраз.
var homoglyphs = map[rune]rune{
	'a': 'а', 'c': 'с', 'e': 'е', 'o': 'о', 'p': 'р', 'x': 'х', 'y': 'у',
	'A': 'А', 'C': 'С', 'E': 'Е', 'O': 'О', 'P': 'Р', 'X': 'Х', 'Y': 'У',
	'B': 'В', 'K': 'К', 'M': 'М', 'H': 'Н', 'T': 'Т',
}

func normalizeHomoglyphs(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { _, ok := homoglyphs[r]; return ok }) {
		return s
	}
	b := make([]rune, 0, len(s))
	for _, r := range s {
		if c, ok := homoglyphs[r]; ok {
			r = c
		}
		b = append(b, r)
	}
	return string(b)
}

// DetectFurnishingDetailed — признак меблировки + уверенность + фразы,
// по которым решено (для метки в БД и отчёта). Полная эвристика: явные
// фразы + перечисление предметов (для классификации арендных аналогов).
func DetectFurnishingDetailed(description string, params []Param) (Furnishing, bool, string) {
	if f, ok, ev := detectExplicit(description, params); ok {
		return f, ok, ev
	}
	// Перечисление предметов мебели: ≥2 разных предмета, среди которых
	// есть хотя бы один «настоящий» (сон/хранение/гостиная) — бытовая
	// техника и кухня без кровати/шкафа меблированной квартиру не делают.
	text := normalizeHomoglyphs(strings.ToLower(description))
	var items []string
	for _, item := range furnitureItems {
		if strings.Contains(text, item) {
			items = append(items, item)
		}
	}
	hasProper := false
	for _, p := range furnitureProper {
		if strings.Contains(text, p) {
			hasProper = true
			break
		}
	}
	if len(items) >= 2 && hasProper {
		return Furnished, true, "описание: предметы мебели (" + strings.Join(items, ", ") + ")"
	}
	return Unknown, false, ""
}

// detectExplicit — только явные маркеры (params и фразы), без вывода по
// предметам. Для ВХОДНОГО объявления продажи: сомнение остаётся сомнением
// («остаётся кухонный гарнитур» ≠ «полностью меблирована»).
func detectExplicit(description string, params []Param) (Furnishing, bool, string) {
	// 1) Явный признак в params (Авито: «Мебель и техника», «Мебель»).
	for _, p := range params {
		title := strings.ToLower(p.Title)
		if !strings.Contains(title, "мебел") {
			continue
		}
		v := strings.ToLower(p.Value)
		switch {
		case strings.Contains(v, "без"):
			return Unfurnished, true, "params: " + p.Title + "=" + p.Value
		case v != "" && v != "нет":
			return Furnished, true, "params: " + p.Title + "=" + p.Value
		}
	}
	// 2) Описание, нормализованное против омоглифов: сначала негативные
	// маркеры (консервативно), затем «обставить под себя», затем позитив.
	text := normalizeHomoglyphs(strings.ToLower(description))
	if hit := firstHit(text, unfurnishedPhrases); hit != "" {
		return Unfurnished, true, "описание: «" + hit + "»"
	}
	if hit := firstHit(text, noFurniturePhrases); hit != "" {
		return Unfurnished, true, "описание: «" + hit + "» (обставить самостоятельно)"
	}
	if hit := firstHit(text, furnishedPhrases); hit != "" {
		return Furnished, true, "описание: «" + hit + "»"
	}
	return Unknown, false, ""
}

// DetectFurnishing — признак + уверенность (совместимый интерфейс).
func DetectFurnishing(description string, params []Param) (Furnishing, bool) {
	f, ok, _ := DetectFurnishingDetailed(description, params)
	return f, ok
}

// ResolveFurnishing — меблировка входного объявления (продажи): полная
// эвристика (явные фразы + предметы). Сомнение консервативно = «без
// мебели» (помечается в отчёте). Надбавка на меблировку добавляется
// только если мебели нет.
func ResolveFurnishing(description string, params []Param) (Furnishing, bool) {
	f, ok, _ := DetectFurnishingDetailed(description, params)
	if !ok {
		return Unfurnished, false
	}
	return f, true
}

func firstHit(text string, phrases []string) string {
	for _, p := range phrases {
		if strings.Contains(text, p) {
			return p
		}
	}
	return ""
}
