package evaluate

import "testing"

// Реальное описание 7907741579 (ЖК Сенат, 8 500 000 ₽; ground truth
// владельца — БЕЗ мебели; issue #110). Мебель упоминается только как
// потенциальная: «ниша под устройство кухонной зоны», «место под
// встроенный шкаф». Насыщено латинскими гомоглифами.
const ad110Description = "Пpодaётся уютная студия в ЖK Сенат с рeмонтoм от зaстpoйщика " +
	"в монолитнo-киpпичнoм дoмe 2025 гoда постpoйки. Дoм сдaн, ключи получены. " +
	"Фотoгрaфии pеaльные. В подъeздe вceго пять квaртиp. Зaкрытaя терpитория двoра. " +
	"Cанузел cовмeщённый, тёплыe полы дoбaвляют кoмфoрта. " +
	"B кoмнaтe ecть удобнaя ниша пoд устpойcтво кухонной зоны. " +
	"В коридоре есть место под встроенный шкаф."

// Кейс #110: объявление без мебели не должно классифицироваться как
// furnished из-за упоминаний потенциальной мебели.
func TestAd110Unfurnished(t *testing.T) {
	f, ok, ev := DetectFurnishingDetailed(ad110Description, nil)
	if f != Unknown && f != Unfurnished {
		t.Fatalf("детектор вернул %s (ok=%v, ev=%q), want unknown/unfurnished: потенциальная мебель («ниша под…», «место под…») не является мебелью", f, ok, ev)
	}
	in, certain := ResolveFurnishing(ad110Description, nil)
	if in != Unfurnished {
		t.Fatalf("ResolveFurnishing = %s, want unfurnished (консервативно)", in)
	}
	if certain {
		t.Fatal("уверенность не должна быть true: явных маркеров мебели в тексте нет")
	}
}

// Потенциальная мебель в разных формулировках не делает квартиру меблированной.
func TestPotentialFurnitureNotFurnished(t *testing.T) {
	cases := []string{
		"В комнате есть ниша под устройство кухонной зоны.",
		"В коридоре место под встроенный шкаф.",
		"Останется место под вашу мебель, купить сможете по своему вкусу.",
		"Сможете поставить диван и кровать по своему вкусу.",
	}
	for _, d := range cases {
		f, ok, _ := DetectFurnishingDetailed(d, nil)
		if ok && f == Furnished {
			t.Errorf("%q: классифицирован как furnished — потенциальная мебель принята за существующую", d)
		}
	}
}
