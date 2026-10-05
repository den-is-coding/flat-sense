// Package roomfurnishing — определение меблировки комнаты по фотографии
// (issue #110, фото-фолбэк когда текст неоднозначен; методика #58 —
// сначала эвристики/линейная модель, за портом). Признаки сцены:
// насыщенность краями (мебель/декор), цветность, разнообразие цветов,
// доля белёсой пустоты.
package roomfurnishing

import (
	"image"
	"math"

	"golang.org/x/image/draw"
)

// Verdict — результат классификации фото комнаты.
type Verdict struct {
	Furnishing string  `json:"furnishing"` // furnished | unfurnished | unknown
	Confidence float64 `json:"confidence"` // 0..1 уверенность в furnished
}

// Features — признаки фото для классификатора.
type Features struct {
	EdgeDensity  float64 `csv:"edge_density"`  // доля пикселей с градиентом > порога (мебель, текстиль, декор)
	Colorfulness float64 `csv:"colorfulness"`  // средний разброс каналов /255
	SatFrac      float64 `csv:"sat_frac"`      // доля явно цветных пикселей
	UniqueColors float64 `csv:"unique_colors"` // квантованные цвета с долей >= 0.2%
	LightFrac    float64 `csv:"light_frac"`    // доля белёсых пикселей (пустые стены/пол)
	FlatFrac     float64 `csv:"flat_frac"`     // доля однородных 5x5 участков (голые стены/пол)
}

const (
	maxSide  = 256
	edgeMin  = 50
	satMin   = 40
	lightMin = 200
)

// Analyze вычисляет признаки фото комнаты.
func Analyze(img image.Image) Features {
	small := downscale(img)
	b := small.Bounds()
	w, h := b.Dx(), b.Dy()
	n := w * h
	if n == 0 {
		return Features{}
	}
	gray := make([]float64, n)
	var colorSum, sat, light int
	var hist [4096]int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl := rgbAt(small, b.Min.X+x, b.Min.Y+y)
			gray[y*w+x] = 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)
			spread := int(max3(r, g, bl)) - int(min3(r, g, bl))
			colorSum += spread
			if spread > satMin {
				sat++
			}
			if int(0.299*float64(r)+0.587*float64(g)+0.114*float64(bl)) >= lightMin {
				light++
			}
			hist[(int(r>>4)<<8)|(int(g>>4)<<4)|int(bl>>4)]++
		}
	}
	unique := 0
	for _, c := range hist {
		if c*500 >= n {
			unique++
		}
	}

	// Плотность краёв по градиенту (перепад яркости соседних пикселей).
	edges := 0
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*w + x
			gx := gray[i+1] - gray[i-1]
			gy := gray[i+w] - gray[i-w]
			if math.Hypot(gx, gy) > edgeMin {
				edges++
			}
		}
	}
	inner := (w - 2) * (h - 2)
	// Доля однородных 5x5 участков: у пустой комнаты стены и пол занимают
	// большую площадь без текстуры, меблированная комната «шумнее».
	flat := 0
	for y := 2; y < h-2; y++ {
		for x := 2; x < w-2; x++ {
			i := y*w + x
			mean := (gray[i] + gray[i-1] + gray[i+1] + gray[i-w] + gray[i+w] +
				gray[i-w-1] + gray[i-w+1] + gray[i+w-1] + gray[i+w+1]) / 9
			dev := math.Abs(gray[i]-mean) + math.Abs(gray[i-1]-mean) + math.Abs(gray[i+1]-mean)
			if dev < 12 {
				flat++
			}
		}
	}
	flatInner := (w - 4) * (h - 4)
	return Features{
		EdgeDensity:  float64(edges) / float64(inner),
		Colorfulness: float64(colorSum) / float64(n) / 255,
		SatFrac:      float64(sat) / float64(n),
		UniqueColors: float64(unique),
		LightFrac:    float64(light) / float64(n),
		FlatFrac:     float64(flat) / float64(flatInner),
	}
}

// Порог уверенности: ниже — unknown (не догадываться, per issue #110).
const unknownBand = 0.60

// Веса логистической модели, подобраны на размеченной выборке
// internal/roomfurnishing/testdata/fixtures (32 фото: 21 furnished /
// 11 unfurnished; метки — по явным фразам «мебель и техника»/«без мебели»
// в объявлениях-источниках). Цветность исключена: пейзаж за окном читался
// как мебель. Метрики на выборке — см. eval_test.go: precision 0.842,
// recall 0.941, 9/32 спорных фото уходят в unknown.
var model = logisticModel{
	bias: -1.5868, edgeDensity: 8.6121, satFrac: 13.8337,
	uniqueColors: 0.0268, lightFrac: -3.3941,
}

type logisticModel struct {
	bias, edgeDensity, satFrac, uniqueColors, lightFrac float64
}

// Classify — чистая функция признаки → вердикт.
func Classify(f Features) Verdict {
	z := model.bias +
		model.edgeDensity*f.EdgeDensity +
		model.satFrac*f.SatFrac +
		model.uniqueColors*f.UniqueColors +
		model.lightFrac*f.LightFrac
	p := 1 / (1 + math.Exp(-z))
	switch {
	case p >= unknownBand:
		return Verdict{Furnishing: "furnished", Confidence: p}
	case p <= 1-unknownBand:
		return Verdict{Furnishing: "unfurnished", Confidence: 1 - p}
	default:
		return Verdict{Furnishing: "unknown", Confidence: p}
	}
}

// Detector — порт фото-детектора меблировки.
type Detector interface {
	Detect(img image.Image) Verdict
}

// HeuristicDetector — линейная модель поверх сценных признаков.
type HeuristicDetector struct{}

func NewHeuristicDetector() HeuristicDetector { return HeuristicDetector{} }

func (HeuristicDetector) Detect(img image.Image) Verdict { return Classify(Analyze(img)) }

var _ Detector = HeuristicDetector{}

func downscale(img image.Image) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	scale := float64(maxSide) / float64(max(w, h))
	if scale >= 1 {
		return img
	}
	nw, nh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

func rgbAt(img image.Image, x, y int) (uint8, uint8, uint8) {
	r, g, b, _ := img.At(x, y).RGBA()
	return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
}

func max3(a, b, c uint8) uint8 {
	if b > a {
		a = b
	}
	if c > a {
		a = c
	}
	return a
}

func min3(a, b, c uint8) uint8 {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
