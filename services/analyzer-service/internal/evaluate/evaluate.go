package evaluate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrNoRentData — валидный исход «по этому ЖК нет арендных данных»
// (не ошибка): API отвечает вежливым отказом, не 500.
var ErrNoRentData = errors.New("no rent data for this residential complex")

// Source — источник данных для оценки (порт; адаптеры: БД avito_listings,
// дампы парсера из каталога — для тестов/локальных прогонов).
type Source interface {
	// ListingByID — входное объявление.
	ListingByID(ctx context.Context, id int64) (*Listing, error)
	// RentListings — весь арендный пул (кластеризация выполняется здесь,
	// в evaluate: дом → студии → диапазон площади).
	RentListings(ctx context.Context) ([]Listing, error)
}

// Config — параметры методики (константы вынесены из кода, см. README).
type Config struct {
	// FurnishingCostRUB — надбавка на меблировку для сценария «с мебелью»,
	// когда мебель в объявлении отсутствует (issue: 500 000 ₽).
	FurnishingCostRUB int64
	// AreaTolerancePct — допуск площади аналогов относительно входного
	// объявления (правила #55, параметр уточняется задачей #55).
	AreaTolerancePct float64
	// MinClusterSize — минимальный кластер для полноценного confidence.
	MinClusterSize int
	// ClusterRadiusM — радиус сопоставления по координатам (фолбэк, когда
	// дом/адрес не сматчились; корпуса одного ЖК обычно в пределах 500 м).
	ClusterRadiusM int
}

// DefaultConfig — значения по умолчанию (продублированы в env-обвязке main.go).
func DefaultConfig() Config {
	return Config{FurnishingCostRUB: 500_000, AreaTolerancePct: 20, MinClusterSize: 3, ClusterRadiusM: 500}
}

// Evaluator — расчёт оценки по объявлению.
type Evaluator struct {
	Source Source
	Config Config
}

// NewEvaluator — конструктор с дефолтным конфигом при нулевых полях.
func NewEvaluator(src Source, cfg Config) *Evaluator {
	if cfg.FurnishingCostRUB == 0 && cfg.AreaTolerancePct == 0 {
		cfg = DefaultConfig()
	}
	if cfg.MinClusterSize == 0 {
		cfg.MinClusterSize = DefaultConfig().MinClusterSize
	}
	if cfg.ClusterRadiusM == 0 {
		cfg.ClusterRadiusM = DefaultConfig().ClusterRadiusM
	}
	return &Evaluator{Source: src, Config: cfg}
}

// Report — результат оценки (тело страницы результата, issue #64).
type Report struct {
	Status string `json:"status"` // ok | no_rent_data
	Notice string `json:"notice,omitempty"`

	Listing *ListingView `json:"listing,omitempty"`

	InputFurnishing Furnishing `json:"inputFurnishing"`
	FurnishingNote  string     `json:"furnishingNote,omitempty"`

	Cluster *ClusterView `json:"cluster,omitempty"`

	Scenarios []Scenario `json:"scenarios,omitempty"`

	Confidence string   `json:"confidence,omitempty"` // high | medium | low
	Warnings   []string `json:"warnings,omitempty"`
}

// ListingView — данные объявления для страницы результата.
type ListingView struct {
	ID       int64   `json:"id"`
	URL      string  `json:"url"`
	Title    string  `json:"title"`
	Photo    string  `json:"photo,omitempty"`
	Price    int64   `json:"price"`
	Area     float64 `json:"area"`
	Rooms    string  `json:"rooms"`
	Floor    string  `json:"floor,omitempty"`
	Address  string  `json:"address"`
	HouseKey string  `json:"houseKey"`
}

// ClusterView — кластер аналогов для страницы результата.
type ClusterView struct {
	HouseKey     string     `json:"houseKey"`
	AreaRange    [2]float64 `json:"areaRange"`
	N            int        `json:"n"`
	NFurnished   int        `json:"nFurnished"`
	NUnfurnished int        `json:"nUnfurnished"`
	Rent         RentStats  `json:"rent"`
}

// EvaluateByID — полная оценка объявления по id (или URL, см. адаптеры).
func (e *Evaluator) EvaluateByID(ctx context.Context, id int64) (*Report, error) {
	input, err := e.Source.ListingByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if input == nil {
		return nil, fmt.Errorf("listing %d not found", id)
	}
	return e.evaluate(ctx, input)
}

func (e *Evaluator) evaluate(ctx context.Context, input *Listing) (*Report, error) {
	rep := &Report{Status: "ok", Listing: listView(input)}

	// Меблировка входного объявления (продажа): консервативно.
	f, certain := ResolveFurnishing(input.Description, input.Params)
	rep.InputFurnishing = f
	if !certain {
		rep.FurnishingNote = "меблировка по объявлению не определена — консервативно считаем «без мебели»"
	}

	// Арендный пул и кластер «тот же дом, студии, диапазон площади».
	rents, err := e.Source.RentListings(ctx)
	if err != nil {
		return nil, err
	}
	cluster := BuildCluster(rents, input, e.Config.AreaTolerancePct, e.Config.ClusterRadiusM)
	stats := cluster.Stats()
	rep.Cluster = &ClusterView{
		HouseKey:     cluster.HouseKey,
		AreaRange:    [2]float64{round1(cluster.AreaMin), round1(cluster.AreaMax)},
		N:            stats.N,
		NFurnished:   stats.NFurnished,
		NUnfurnished: stats.NUnfurnished,
		Rent:         stats,
	}

	if stats.N == 0 {
		return &Report{
			Status: "no_rent_data",
			Notice: fmt.Sprintf(
				"по дому/ЖК %s нет арендных данных о студиях в диапазоне %.1f–%.1f м² — окупаемость не считаем, это валидный исход",
				cluster.HouseKey, cluster.AreaMin, cluster.AreaMax),
			Listing:         rep.Listing,
			InputFurnishing: rep.InputFurnishing,
			FurnishingNote:  rep.FurnishingNote,
		}, nil
	}

	// Сценарии. Медианы берутся из строгих групп по мебели; если группа
	// пуста, а кластер не пуст — фолбэк на медиану всего кластера с
	// предупреждением (в реальных объявлениях мебель часто не указана).
	price := input.Price
	if price <= 0 {
		return nil, fmt.Errorf("listing %d has no price", input.ID)
	}
	furnMed, furnOK := cluster.groupMedian(cluster.Furnished)
	unfMed, unfOK := cluster.groupMedian(cluster.Unfurn)
	furnP := groupBounds(cluster.Furnished)
	unfP := groupBounds(cluster.Unfurn)
	furnComps, unfComps := len(cluster.Furnished), len(cluster.Unfurn)

	if !unfOK && stats.N > 0 {
		unfMed, unfP, unfComps = stats.Median, bounds{p25: stats.P25, p75: stats.P75}, stats.N
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"меблировка не указана у %d из %d арендных аналогов — медиана «без мебели» посчитана по всему кластеру", stats.N-stats.NFurnished-stats.NUnfurnished, stats.N))
	}
	if !furnOK && stats.N > 0 {
		furnMed, furnP, furnComps = stats.Median, bounds{p25: stats.P25, p75: stats.P75}, stats.N
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"в кластере нет арендных объявлений с явно указанной мебелью (%d без указания) — медиана «с мебелью» посчитана по всему кластеру", stats.N-stats.NFurnished-stats.NUnfurnished))
	}

	if f == Furnished {
		// Мебель есть: только сценарий «с мебелью», без надбавки (issue п.5).
		rep.Scenarios = append(rep.Scenarios,
			ComputeScenario("с мебелью", furnMed, furnP.p25, furnP.p75, furnComps, price, 0))
	} else {
		// Мебели нет (или сомнение): оба сценария (issue п.3–4).
		rep.Scenarios = append(rep.Scenarios,
			ComputeScenario("без мебели", unfMed, unfP.p25, unfP.p75, unfComps, price, 0))
		rep.Scenarios = append(rep.Scenarios,
			ComputeScenario("с мебелью (после меблировки)", furnMed, furnP.p25, furnP.p75, furnComps, price, e.Config.FurnishingCostRUB))
	}

	// Confidence по размеру кластера (метрики качества — задача #55).
	switch {
	case stats.N < e.Config.MinClusterSize:
		rep.Confidence = "low"
		rep.Warnings = append(rep.Warnings, warningMinCluster(stats.N, e.Config.MinClusterSize))
	case stats.N < 2*e.Config.MinClusterSize:
		rep.Confidence = "medium"
	default:
		rep.Confidence = "high"
	}
	return rep, nil
}

type bounds struct{ p25, p75 float64 }

// groupBounds — p25/p75 группы (для вилки сценария).
func groupBounds(l []Listing) bounds {
	if len(l) == 0 {
		return bounds{}
	}
	prices := make([]float64, 0, len(l))
	for i := range l {
		prices = append(prices, float64(l[i].Price))
	}
	sort.Float64s(prices)
	return bounds{p25: quantile(prices, 0.25), p75: quantile(prices, 0.75)}
}

func listView(l *Listing) *ListingView {
	v := &ListingView{
		ID:       l.ID,
		URL:      l.URL,
		Title:    strings.TrimSpace(l.Title),
		Price:    l.Price,
		Area:     l.TotalArea,
		Rooms:    roomsLabel(l),
		Address:  strings.TrimSpace(l.Address),
		HouseKey: l.HouseKey(),
	}
	if l.Floor != nil {
		if l.FloorsTotal != nil {
			v.Floor = fmt.Sprintf("%d/%d", *l.Floor, *l.FloorsTotal)
		} else {
			v.Floor = fmt.Sprintf("%d", *l.Floor)
		}
	}
	if len(l.Images) > 0 {
		v.Photo = l.Images[0].URL
	}
	return v
}

func roomsLabel(l *Listing) string {
	if l.IsStudio() {
		return "студия"
	}
	if l.Rooms != nil {
		return fmt.Sprintf("%d-к", *l.Rooms)
	}
	return "—"
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
