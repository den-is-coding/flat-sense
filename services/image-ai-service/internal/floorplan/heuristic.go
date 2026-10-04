package floorplan

import (
	"fmt"
	"image"
	"math"

	xdraw "golang.org/x/image/draw"
)

// Metrics — числовые признаки изображения, по которым эвристика
// отличает чертёжную графику планировки от фотографии комнаты.
// Экспортируются для отладки/разметки (CSV-дампы, тюнинг порогов).
type Metrics struct {
	LightFrac    float64 `csv:"light_frac"`    // доля почти белых пикселей (L >= lightMin)
	DarkFrac     float64 `csv:"dark_frac"`     // доля тёмных пикселей (L <= darkMax) — линии, текст
	Colorfulness float64 `csv:"colorfulness"`  // средний разброс каналов (max-min)/255
	SatFrac      float64 `csv:"sat_frac"`      // доля явно цветных пикселей (spread > satMin)
	UniqueColors float64 `csv:"unique_colors"` // число квантованных цветов с долей >= 0.2%
	EdgeFrac     float64 `csv:"edge_frac"`     // доля пикселей с градиентом > edgeMin
	StraightFrac float64 `csv:"straight_frac"` // доля граничных пикселей в длинных Г/В отрезках
	Noise        float64 `csv:"noise"`         // средний ВЧ-остаток на неграничных пикселях (фотошум, текстура)
}

// String — человекочитаемый дамп для логов.
func (m Metrics) String() string {
	return fmt.Sprintf("light=%.3f dark=%.3f color=%.3f sat=%.3f colors=%.0f edge=%.3f straight=%.3f noise=%.2f",
		m.LightFrac, m.DarkFrac, m.Colorfulness, m.SatFrac, m.UniqueColors, m.EdgeFrac, m.StraightFrac, m.Noise)
}

const (
	analyzeMaxSide = 256 // рабочее разрешение после даунскейла

	lightMin = 195 // порог «почти белый»
	darkMax  = 90  // порог «тёмная линия/текст»
	satMin   = 40  // порог «явно цветной» (разброс каналов 0..255)
	edgeMin  = 50  // порог градиента Собеля
	minRun   = 12  // длина Г/В отрезка, считающегося «линией чертежа»
)

// Analyze вычисляет Metrics для изображения (любого размера; внутри —
// даунскейл до analyzeMaxSide по длинной стороне).
func Analyze(img image.Image) Metrics {
	small := downscale(img, analyzeMaxSide)
	b := small.Bounds()
	w, h := b.Dx(), b.Dy()
	n := w * h
	if n == 0 {
		return Metrics{}
	}

	gray := make([]float64, n)
	var (
		light, dark, sat, colorSum int
		hist                       [4096]int // 4 бита на канал
	)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := small.At(b.Min.X+x, b.Min.Y+y).RGBA()
			r8, g8, b8 := uint8(r>>8), uint8(g>>8), uint8(bl>>8)
			l := (int(r8)*299 + int(g8)*587 + int(b8)*114) / 1000
			gray[y*w+x] = float64(l)
			if l >= lightMin {
				light++
			}
			if l <= darkMax {
				dark++
			}
			spread := int(max3(r8, g8, b8)) - int(min3(r8, g8, b8))
			colorSum += spread
			if spread > satMin {
				sat++
			}
			hist[(int(r8>>4)<<8)|(int(g8>>4)<<4)|int(b8>>4)]++
		}
	}
	unique := 0
	for _, c := range hist {
		if c*500 >= n { // доля >= 0.2%
			unique++
		}
	}

	// Размытие 3x3 + Собель: карта границ.
	blur := boxBlur3(gray, w, h)
	edges := make([]bool, n)
	edgeCount, straightCount := 0, 0
	var noiseSum float64
	noisePix := 0
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*w + x
			gx := sobel(blur, w, x-1, y-1, x+1, y+1, sobelX)
			gy := sobel(blur, w, x-1, y-1, x+1, y+1, sobelY)
			mag := math.Hypot(gx, gy)
			if mag > edgeMin {
				edges[i] = true
				edgeCount++
			} else {
				// ВЧ-остаток считаем только на гладких участках:
				// шум камеры/текстура против чистой подложки чертежа.
				noiseSum += math.Abs(gray[i] - blur[i])
				noisePix++
			}
		}
	}
	// Длинные горизонтальные/вертикальные отрезки среди граничных пикселей.
	for y := 1; y < h-1; y++ {
		run := 0
		for x := 1; x < w-1; x++ {
			if edges[y*w+x] {
				run++
			} else {
				if run >= minRun {
					straightCount += run
				}
				run = 0
			}
		}
		if run >= minRun {
			straightCount += run
		}
	}
	for x := 1; x < w-1; x++ {
		run := 0
		for y := 1; y < h-1; y++ {
			if edges[y*w+x] {
				run++
			} else {
				if run >= minRun {
					straightCount += run
				}
				run = 0
			}
		}
		if run >= minRun {
			straightCount += run
		}
	}

	noise := 0.0
	if noisePix > 0 {
		noise = noiseSum / float64(noisePix)
	}
	m := Metrics{
		LightFrac:    float64(light) / float64(n),
		DarkFrac:     float64(dark) / float64(n),
		Colorfulness: float64(colorSum) / float64(n) / 255,
		SatFrac:      float64(sat) / float64(n),
		UniqueColors: float64(unique),
		EdgeFrac:     float64(edgeCount) / float64(n),
		Noise:        noise,
	}
	if edgeCount > 0 {
		m.StraightFrac = float64(straightCount) / float64(edgeCount)
	}
	return m
}

// HeuristicDetector — адаптер Detector на эвристиках (первый этап,
// см. issue #58): белая подложка, мало цветов, тонкие тёмные линии,
// длинные прямые отрезки, отсутствие фотошума/насыщенных цветов.
type HeuristicDetector struct{}

// NewHeuristicDetector возвращает эвристический адаптер.
func NewHeuristicDetector() HeuristicDetector { return HeuristicDetector{} }

// Detect классифицирует изображение как планировку (IsPlan=true) или
// фотографию, с уверенностью 0..1. Method — floorplan.MethodHeuristic.
func (HeuristicDetector) Detect(img image.Image) Verdict {
	m := Analyze(img)
	isPlan, conf := Classify(m)
	return Verdict{IsPlan: isPlan, Confidence: conf, Method: MethodHeuristic}
}

// Веса логистической модели поверх признаков. Подобраны на размеченном
// наборе fixtures (140 фото реальных объявлений, 79 планов) градиентным
// спуском; на нём же фиксируются метрики в eval_test.go:
// precision 0.938, recall 0.950 (гейт теста — >= 0.90).
var heuristicWeights = logisticModel{
	bias:       -8.7164,
	lightFrac:  7.1008,
	darkFrac:   6.2592,
	colorful:   -60.4714,
	satFrac:    17.4350,
	uniqueClrs: -0.0693,
	edgeFrac:   7.1462,
	straight:   6.6616,
	noise:      0.8354,
}

type logisticModel struct {
	bias, lightFrac, darkFrac, colorful, satFrac, uniqueClrs, edgeFrac, straight, noise float64
}

// Classify — чистая функция признак → вердикт (отдельно от Analyze для
// тестируемости и будущего ML-адаптера с той же сигнатурой).
func Classify(m Metrics) (bool, float64) {
	z := heuristicWeights.bias +
		heuristicWeights.lightFrac*m.LightFrac +
		heuristicWeights.darkFrac*m.DarkFrac +
		heuristicWeights.colorful*m.Colorfulness +
		heuristicWeights.satFrac*m.SatFrac +
		heuristicWeights.uniqueClrs*m.UniqueColors +
		heuristicWeights.edgeFrac*m.EdgeFrac +
		heuristicWeights.straight*m.StraightFrac +
		heuristicWeights.noise*m.Noise
	p := 1 / (1 + math.Exp(-z))
	return p >= 0.5, p
}

// --- вспомогательные ---

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

func downscale(img image.Image, maxSide int) image.Image {
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
	// ApproxBiLinear даёт достаточное качество для статистических признаков.
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

// boxBlur3 — простое сглаживание 3x3 (среднее), для устойчивого Собеля.
func boxBlur3(src []float64, w, h int) []float64 {
	dst := make([]float64, len(src))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum float64
			var cnt int
			for dy := -1; dy <= 1; dy++ {
				yy := y + dy
				if yy < 0 || yy >= h {
					continue
				}
				for dx := -1; dx <= 1; dx++ {
					xx := x + dx
					if xx < 0 || xx >= w {
						continue
					}
					sum += src[yy*w+xx]
					cnt++
				}
			}
			dst[y*w+x] = sum / float64(cnt)
		}
	}
	return dst
}

type sobelKernel int

const (
	sobelX sobelKernel = iota
	sobelY
)

var (
	sobelXK = [3][3]int{{-1, 0, 1}, {-2, 0, 2}, {-1, 0, 1}}
	sobelYK = [3][3]int{{-1, -2, -1}, {0, 0, 0}, {1, 2, 1}}
)

func sobel(src []float64, w, x0, y0, _, _ int, k sobelKernel) float64 {
	var sum float64
	m := sobelXK
	if k == sobelY {
		m = sobelYK
	}
	for dy := 0; dy < 3; dy++ {
		for dx := 0; dx < 3; dx++ {
			sum += float64(m[dy][dx]) * src[(y0+dy)*w+(x0+dx)]
		}
	}
	return sum
}
