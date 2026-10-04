package floorplan

import (
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// Порт должен удовлетворяться эвристическим адаптером.
var _ Detector = HeuristicDetector{}

// solid заливает прямоугольник цветом.
func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// syntheticPlan — белая подложка с тонкими тёмными линиями комнат
// и «размерными» штрихами: типичная чертёжная графика планировки.
func syntheticPlan(w, h int, rng *rand.Rand) *image.RGBA {
	img := solid(w, h, color.RGBA{250, 250, 248, 255})
	ink := color.RGBA{40, 40, 40, 255}
	// Сетка комнат.
	xs := []int{w / 5, 2 * w / 5, 3 * w / 5, 4 * w / 5}
	ys := []int{h / 3, 2 * h / 3}
	for _, x := range xs {
		vline(img, x, 0, h-1, ink)
	}
	for _, y := range ys {
		hline(img, y, 0, w-1, ink)
	}
	vline(img, 2, 0, h-1, ink)
	vline(img, w-3, 0, h-1, ink)
	hline(img, 2, 0, w-1, ink)
	hline(img, h-3, 0, w-1, ink)
	// «Размерные подписи»: короткие чёрные кластеры.
	for i := 0; i < 30; i++ {
		x := rng.Intn(w-10) + 2
		y := rng.Intn(h-10) + 2
		for dy := 0; dy < 4; dy++ {
			for dx := 0; dx < 6; dx++ {
				if (dx+dy)%2 == 0 {
					img.SetRGBA(x+dx, y+dy, ink)
				}
			}
		}
	}
	return img
}

// syntheticPhoto — «фотография»: цветные градиенты + фотошум.
func syntheticPhoto(w, h int, rng *rand.Rand) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r := uint8(90 + 60*x/w)
			g := uint8(70 + 50*y/h)
			b := uint8(120 + 40*(x+y)/(w+h))
			img.SetRGBA(x, y, color.RGBA{r, g, b, 255})
		}
	}
	for i := 0; i < w*h/3; i++ { // сенсорный шум
		x, y := rng.Intn(w), rng.Intn(h)
		c := img.RGBAAt(x, y)
		n := uint8(rng.Intn(30))
		img.SetRGBA(x, y, color.RGBA{c.R + n, c.G + n, c.B + n, 255})
	}
	return img
}

func vline(img *image.RGBA, x, y0, y1 int, c color.RGBA) {
	for y := y0; y <= y1; y++ {
		img.SetRGBA(x, y, c)
	}
}

func hline(img *image.RGBA, y, x0, x1 int, c color.RGBA) {
	for x := x0; x <= x1; x++ {
		img.SetRGBA(x, y, c)
	}
}

func TestHeuristicSynthetic(t *testing.T) {
	rng := rand.New(rand.NewSource(58))
	det := NewHeuristicDetector()

	if v := det.Detect(syntheticPlan(320, 240, rng)); !v.IsPlan || v.Confidence < 0.9 {
		t.Fatalf("чертёж не распознан: %+v", v)
	}
	if v := det.Detect(syntheticPhoto(320, 240, rng)); v.IsPlan || v.Confidence > 0.1 {
		t.Fatalf("фото распознано как план: %+v", v)
	}
	// Уверенность на чертеже должна уверенно превосходить фото.
	plan := det.Detect(syntheticPlan(320, 240, rng))
	photo := det.Detect(syntheticPhoto(320, 240, rng))
	if plan.Confidence <= photo.Confidence {
		t.Fatalf("уверенность план(%+.2f) <= фото(%+.2f)", plan.Confidence, photo.Confidence)
	}
}

// TestEvalFixtures — гейт качества на размеченном наборе реальных фото
// (issue #58): precision >= 0.90 и recall >= 0.90. Набор: 140 фото из
// реальных объявлений (79 планировок), ручная разметка.
func TestEvalFixtures(t *testing.T) {
	blob, err := os.ReadFile(filepath.Join("testdata", "fixtures", "manifest.json"))
	if err != nil {
		t.Skipf("fixtures недоступны: %v", err)
	}
	var entries []struct {
		PhotoRef string `json:"photo_ref"`
		File     string `json:"file"`
		Label    string `json:"label"`
	}
	if err := json.Unmarshal(blob, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) < 50 {
		t.Fatalf("тестовый набор слишком мал: %d < 50", len(entries))
	}

	det := NewHeuristicDetector()
	var tp, fp, tn, fn int
	for _, e := range entries {
		f, err := os.Open(filepath.Join("testdata", "fixtures", filepath.FromSlash(e.File)))
		if err != nil {
			t.Fatalf("%s: %v", e.File, err)
		}
		img, err := jpeg.Decode(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", e.File, err)
		}
		got := det.Detect(img).IsPlan
		want := e.Label == "plan"
		switch {
		case want && got:
			tp++
		case !want && got:
			fp++
			t.Logf("FP %s (%s)", e.File, e.PhotoRef)
		case want && !got:
			fn++
			t.Logf("FN %s", e.File)
		default:
			tn++
		}
	}
	precision := float64(tp) / float64(tp+fp)
	recall := float64(tp) / float64(tp+fn)
	t.Logf("итог: всего=%d TP=%d FP=%d TN=%d FN=%d precision=%.3f recall=%.3f",
		len(entries), tp, fp, tn, fn, precision, recall)
	if precision < 0.90 {
		t.Errorf("precision %.3f < 0.90 (FP=%d)", precision, fp)
	}
	if recall < 0.90 {
		t.Errorf("recall %.3f < 0.90 (FN=%d)", recall, fn)
	}
}
