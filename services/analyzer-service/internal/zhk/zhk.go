// Package zhk — реестр жилых комплексов: каноничное имя, варианты
// названия (алиасы) и адреса корпусов. Адреса корпусов — те же ключи
// кластеризации уровня ЖК, что meta.addresses арендных кампаний в #64
// (адрес любого корпуса в алиасах объявления → совпадение с любым другим
// корпусом того же ЖК).
//
// Источник адресов — дампы кампаний
// parser-service/internal/avito/testdata/avito_run_arenda_*.json (meta.addresses)
// и zhk_addresses.txt кампании «Титул». Новые алиасы при обнаружении
// добавляются сюда (issue #66: в админ-коде ЖК не хардкодятся).
package zhk

import (
	"strings"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// Complex — запись реестра: один ЖК.
type Complex struct {
	// Name — каноничное имя (для отчётов backfill).
	Name string
	// Aliases — варианты названия ЖК: подстрока нормализованного
	// «название ЖК / описание / заголовок». Без коротких слов-омонимов
	// («титул» без уточнения ловит «титульное страхование»).
	Aliases []string
	// Addresses — адреса корпусов ЖК (нормализуются как ключи кластеризации).
	Addresses []string
}

// Registry — все известные ЖК. Целевые четыре ЖК issue #66 — первые.
var Registry = []*Complex{
	{
		Name: "Прайм Приморский",
		Aliases: []string{
			"прайм приморский", "приморский прайм",
		},
		Addresses: []string{
			"Санкт-Петербург, Парашютная ул., 71к2",
			"Санкт-Петербург, Парашютная ул., 77к1",
			"Санкт-Петербург, Парашютная ул., 79к1",
			"Санкт-Петербург, ул. Лидии Зверевой, 6",
		},
	},
	{
		Name: "Титул",
		Aliases: []string{
			"титул в московском", "жк титул",
		},
		Addresses: []string{
			"ул. Кубинская, стр. 1",
			"Ул. Кубинская, стр. 2.1-2.6",
		},
	},
	{
		Name: "Пульс Премьер",
		Aliases: []string{
			"пульс премьер", "премьер пульс",
		},
		Addresses: []string{
			"Санкт-Петербург, Архивная ул., 4",
			"Санкт-Петербург, Архивная ул., 6",
			"Санкт-Петербург, Дальневосточный пр-т, 19к1",
			"Санкт-Петербург, Дальневосточный пр-т, 23",
			"Санкт-Петербург, Ультрамариновая ул., 5",
		},
	},
	{
		Name: "Граффити",
		Aliases: []string{
			"граффити", "graffiti",
		},
		Addresses: []string{
			"Санкт-Петербург, Парашютная ул., 42к1",
			"Санкт-Петербург, Парашютная ул., 42к2",
			"Санкт-Петербург, Парашютная ул., 42к3",
		},
	},
	{
		Name: "CUBE",
		Aliases: []string{
			"cube",
		},
		Addresses: []string{
			"Санкт-Петербург, Кубинская ул., 82к3с1",
		},
	},
	{
		Name: "Сенат",
		Aliases: []string{
			"сенат",
		},
		// meta.addresses кампании «Сенат» пуста; адреса добавляются
		// при обнаружении.
	},
}

// foldLatin — латинские двойники → кириллица (Авито разбавляет слова
// латиницей: «Грaффити», «ЖK»; без свёртки подстрока не находится).
var foldLatin = map[rune]rune{
	'a': 'а', 'b': 'в', 'c': 'с', 'e': 'е', 'h': 'н', 'k': 'к',
	'm': 'м', 'o': 'о', 'p': 'р', 't': 'т', 'x': 'х', 'y': 'у',
}

// NormName — нормализация текста для сопоставления названий: свёртка
// латинских двойников и та же чистка, что у адресов кластеризации.
func NormName(s string) string {
	s = strings.ToLower(s)
	r := []rune(s)
	for i, c := range r {
		if f, ok := foldLatin[c]; ok {
			r[i] = f
		}
	}
	return evaluate.NormalizeAddr(string(r))
}

var registryAddr = func() map[string]*Complex {
	m := map[string]*Complex{}
	for _, c := range Registry {
		for _, a := range c.Addresses {
			m[evaluate.NormalizeAddr(a)] = c
		}
	}
	return m
}()

// Match — к какому ЖК относится объявление: адрес корпуса из реестра
// либо вариант названия в «название ЖК»/описании/заголовке. Не из
// реестра → nil.
func Match(l *evaluate.Listing) *Complex {
	if a := evaluate.NormalizeAddr(l.Address); a != "" {
		if c, ok := registryAddr[a]; ok {
			return c
		}
	}
	var hay strings.Builder
	hay.WriteString(NormName(l.ResidentialComplex))
	hay.WriteString(" ")
	hay.WriteString(NormName(l.Title))
	hay.WriteString(" ")
	hay.WriteString(NormName(l.Description))
	hs := hay.String()
	for _, c := range Registry {
		for _, al := range c.Aliases {
			if strings.Contains(hs, NormName(al)) {
				return c
			}
		}
	}
	return nil
}

// Enrich — Match и (при совпадении) прописать объявлению алиасы — адреса
// всех корпусов ЖК (как это делал DumpSource из meta.addresses кампании):
// студия любого корпуса кластеризуется на уровне ЖК. Возвращает матч.
func Enrich(l *evaluate.Listing) *Complex {
	c := Match(l)
	if c != nil && len(c.Addresses) > 0 {
		l.Aliases = append(l.Aliases, c.Addresses...)
	}
	return c
}
