package roomfurnishing

import (
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

// Гейт качества на размеченной выборке (issue #110: замер precision/recall
// на ≥30 объявлениях). Выборка: 32 фото комнат из реальных объявлений,
// метки — по явным фразам меблировки в текстах источников.
// Спорные фото (уверенность в полосе 0.4–0.6) не считаются ни ошибкой,
// ни попаданием — детектор обязан уходить в unknown, а не угадывать.
func TestEvalFixtures(t *testing.T) {
	var tp, fp, tn, fn, unknown int
	for _, cls := range []string{"furnished", "unfurnished"} {
		entries, err := os.ReadDir(filepath.Join("testdata", "fixtures", cls))
		if err != nil {
			t.Skipf("fixtures недоступны: %v", err)
		}
		for _, e := range entries {
			if filepath.Ext(e.Name()) != ".jpg" {
				continue
			}
			f, err := os.Open(filepath.Join("testdata", "fixtures", cls, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			img, err := jpeg.Decode(f)
			f.Close()
			if err != nil {
				t.Fatalf("%s/%s: %v", cls, e.Name(), err)
			}
			v := NewHeuristicDetector().Detect(img)
			switch {
			case v.Furnishing == "unknown":
				unknown++
			case cls == "furnished" && v.Furnishing == "furnished":
				tp++
			case cls == "furnished":
				fn++
				t.Logf("FN %s p=%.2f", e.Name(), v.Confidence)
			case v.Furnishing == "unfurnished":
				tn++
			default:
				fp++
				t.Logf("FP %s p=%.2f", e.Name(), v.Confidence)
			}
		}
	}
	total := tp + fp + tn + fn + unknown
	if total < 30 {
		t.Fatalf("выборка мала: %d < 30", total)
	}
	precision := float64(tp) / float64(tp+fp)
	recall := float64(tp) / float64(tp+fn)
	t.Logf("итог: всего=%d TP=%d FP=%d TN=%d FN=%d UNKNOWN=%d precision=%.3f recall=%.3f",
		total, tp, fp, tn, fn, unknown, precision, recall)
	if precision < 0.85 {
		t.Errorf("precision %.3f < 0.85", precision)
	}
	if recall < 0.90 {
		t.Errorf("recall %.3f < 0.90", recall)
	}
	if unknown*2 > total {
		t.Errorf("unknown слишком много: %d/%d", unknown, total)
	}
}
