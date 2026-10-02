package avito

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
)

// Авито отдаёт данные двумя способами (используем оба):
//  1. современный SSR: <script type="mime/invalid" data-mfe-state="true">{json}</script>
//     (содержимое — HTML-экранированный JSON, путь loaderData.data.catalog.items);
//  2. легаси: window.__initialData__ = "..." — URI-кодированный JSON в строке.
var (
	reMFEScript = regexp.MustCompile(`(?is)<script[^>]*data-mfe-state=["']true["'][^>]*>(.*?)</script>`)
	reInitial   = regexp.MustCompile(`(?is)window\.__initialData__\s*=\s*"(.*?)"\s*;`)
)

// extractStates вытаскивает все JSON-состояния из HTML страницы
// (каталога или карточки). Возвращает слайс распарсенных деревьев.
// Скрипты с "sandbox" в тексте пропускаются — как в эталонных парсерах.
func extractStates(pageHTML string) ([]any, error) {
	var out []any
	for _, raw := range reMFEScript.FindAllStringSubmatch(pageHTML, -1) {
		if strings.Contains(raw[0], "sandbox") {
			continue
		}
		s := html.UnescapeString(raw[1])
		var v any
		if err := json.Unmarshal([]byte(s), &v); err == nil {
			out = append(out, v)
		}
	}
	for _, raw := range reInitial.FindAllStringSubmatch(pageHTML, -1) {
		s := html.UnescapeString(raw[1])
		if dec, err := url.QueryUnescape(s); err == nil {
			s = dec
		}
		var v any
		if err := json.Unmarshal([]byte(s), &v); err == nil {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no state scripts found in page (blocked or markup changed)")
	}
	return out, nil
}

// ---- гибкий обход JSON-деревьев ----
// Схема данных Авито меняется между релизами, поэтому вместо жёстких путей
// ищем значения по именам ключей на разумной глубине.

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asArray(v any) ([]any, bool) {
	a, ok := v.([]any)
	return a, ok
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case bool:
		return fmt.Sprintf("%t", t)
	}
	return ""
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.ReplaceAll(t, ",", "."), "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case float64:
		return t != 0
	}
	return false
}

// findKey рекурсивно ищет первый объект с ключом key и возвращает его значение.
func findKey(v any, key string) (any, bool) {
	switch t := v.(type) {
	case map[string]any:
		if val, ok := t[key]; ok {
			return val, true
		}
		// предпочтительный порядок обхода: известные контейнеры первыми
		for _, container := range []string{"loaderData", "data", "catalog", "result", "state", "props", "initialState"} {
			if child, ok := t[container]; ok {
				if val, ok := findKey(child, key); ok {
					return val, true
				}
			}
		}
		for _, val := range t {
			if val2, ok := findKey(val, key); ok {
				return val2, true
			}
		}
	case []any:
		for _, item := range t {
			if val, ok := findKey(item, key); ok {
				return val, true
			}
		}
	}
	return nil, false
}

// collectMapsByKey возвращает все объекты-значения ключа key во всём дереве.
func collectMapsByKey(v any, key string) []map[string]any {
	var out []map[string]any
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, val := range t {
				if k == key {
					if m, ok := asMap(val); ok {
						out = append(out, m)
						continue
					}
				}
				walk(val)
			}
		case []any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(v)
	return out
}

// collectMapsWithKey возвращает все объекты, у которых ЕСТЬ ключ key
// (сам объект, а не значение ключа).
func collectMapsWithKey(v any, key string) []map[string]any {
	var out []map[string]any
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if _, ok := t[key]; ok {
				out = append(out, t)
			}
			for _, val := range t {
				walk(val)
			}
		case []any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(v)
	return out
}

// deepGet возвращает m[key], если это объект.
func deepGet(m map[string]any, key string) (map[string]any, bool) {
	if m == nil {
		return nil, false
	}
	if v, ok := m[key]; ok {
		return asMap(v)
	}
	return nil, false
}

func mapGet(m map[string]any, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}

func mapStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	return asString(m[key])
}

func mapFloat(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	return asFloat(m[key])
}

// findItemsArray ищет в дереве массив выдачи: массив объектов, у которых есть
// идентификатор (id/itemId) и признак объявления (title/priceDetailed/urlPath).
func findItemsArray(states []any) []any {
	for _, st := range states {
		for _, arr := range collectArraysByKey(st, "items") {
			if looksLikeListingArray(arr) {
				return arr
			}
		}
		// некоторые версии кладут выдачу в itemsList / serpItems
		for _, key := range []string{"itemsList", "serpItems"} {
			for _, arr := range collectArraysByKey(st, key) {
				if looksLikeListingArray(arr) {
					return arr
				}
			}
		}
	}
	return nil
}

func collectArraysByKey(v any, key string) [][]any {
	var out [][]any
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, val := range t {
				if k == key {
					if a, ok := asArray(val); ok {
						out = append(out, a)
					}
				}
				walk(val)
			}
		case []any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(v)
	return out
}

func looksLikeListingArray(arr []any) bool {
	if len(arr) == 0 {
		return false
	}
	good := 0
	for _, it := range arr {
		m, ok := asMap(it)
		if !ok {
			continue
		}
		hasID := mapStr(m, "id") != "" || mapStr(m, "itemId") != ""
		hasKind := mapStr(m, "title") != "" || mapGet(m, "priceDetailed") != nil || mapStr(m, "urlPath") != ""
		if hasID && hasKind {
			good++
		}
	}
	return good >= len(arr)/2 && good > 0
}
