package evaluate

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	// MinClusterSize — минимальный кластер для полноценного confidence.
	MinClusterSize int
	// ClusterRadiusM — радиус сопоставления по координатам (фолбэк, когда
	// дом/адрес не сматчились; корпуса одного ЖК обычно в пределах 500 м).
	ClusterRadiusM int
	// RealtorFeePct — комиссия риэлтору, % от цены квартиры.
	RealtorFeePct float64
	// TitleInsurancePct — титульное страхование, % от цены квартиры.
	TitleInsurancePct float64
	// DealFixedCostsRUB — издержки на оформление сделки, фикс.
	DealFixedCostsRUB int64
}

// DefaultConfig — значения по умолчанию (продублированы в env-обвязке main.go).
func DefaultConfig() Config {
	return Config{
		FurnishingCostRUB: 500_000, MinClusterSize: 3,
		ClusterRadiusM: 500, RealtorFeePct: 3, TitleInsurancePct: 1,
		DealFixedCostsRUB: 25_000,
	}
}

// DealCosts — транзакционные издержки покупки: комиссия риэлтору и
// титульное страхование считаются от цены квартиры, оформление — фикс.
func (c Config) DealCosts(price int64) int64 {
	pct := c.RealtorFeePct + c.TitleInsurancePct
	percentPart := math.Round(float64(price) * pct / 100)
	return int64(percentPart) + c.DealFixedCostsRUB
}

// Evaluator — расчёт оценки по объявлению.
type Evaluator struct {
	Source Source
	Config Config
}

// NewEvaluator — конструктор с дефолтным конфигом при нулевых полях.
func NewEvaluator(src Source, cfg Config) *Evaluator {
	if cfg.FurnishingCostRUB == 0 && cfg.MinClusterSize == 0 {
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

	// Меблировка входного объявления (продажа): полная эвристика
	// (явные фразы + предметы); сомнение консервативно = «без мебели».
	f, confident, evidence := DetectFurnishingDetailed(input.Description, input.Params)
	if !confident {
		f = Unfurnished
	}
	rep.InputFurnishing = f
	switch {
	case !confident:
		rep.FurnishingNote = "меблировка по объявлению не определена — консервативно считаем «без мебели», добавляем надбавку на меблировку"
	case f == Furnished && strings.HasPrefix(evidence, "описание: предметы"):
		rep.FurnishingNote = "мебель определена по перечислению предметов в описании — надбавка на меблировку не добавляется"
	}

	// Арендный пул и кластер «студии того же ЖК/дома» (все, без фильтра
	// по площади — фактический разброс площадей идёт на страницу справкой).
	rents, err := e.Source.RentListings(ctx)
	if err != nil {
		return nil, err
	}
	cluster := BuildCluster(rents, input, e.Config.ClusterRadiusM)
	stats := cluster.Stats()
	lo, hi := cluster.AreaRange()
	rep.Cluster = &ClusterView{
		HouseKey:     cluster.HouseKey,
		AreaRange:    [2]float64{round1(lo), round1(hi)},
		N:            stats.N,
		NFurnished:   stats.NFurnished,
		NUnfurnished: stats.NUnfurnished,
		Rent:         stats,
	}

	if stats.N == 0 {
		return &Report{
			Status: "no_rent_data",
			Notice: fmt.Sprintf(
				"по дому/ЖК %s нет арендных данных о студиях — окупаемость не считаем, это валидный исход",
				cluster.HouseKey),
			Listing:         rep.Listing,
			InputFurnishing: rep.InputFurnishing,
			FurnishingNote:  rep.FurnishingNote,
		}, nil
	}

	// Сценарии. Медианы берутся из строгих групп по мебели; если группа
	// пуста — сценарий помечается «нет данных» с явной причиной
	// (числа не выдумываем), даже если кластер в целом не пуст.
	price := input.Price
	if price <= 0 {
		return nil, fmt.Errorf("listing %d has no price", input.ID)
	}
	dealCosts := e.Config.DealCosts(price)
	furnMed, _ := cluster.groupMedian(cluster.Furnished)
	unfMed, _ := cluster.groupMedian(cluster.Unfurn)
	furnP := groupBounds(cluster.Furnished)
	unfP := groupBounds(cluster.Unfurn)
	furnComps, unfComps := len(cluster.Furnished), len(cluster.Unfurn)
	unknown := stats.N - stats.NFurnished - stats.NUnfurnished

	if f == Furnished {
		// Мебель есть: только сценарий «с мебелью», без надбавки (issue п.5).
		s := ComputeScenario("с мебелью", furnMed, furnP.p25, furnP.p75, furnComps, price, 0, dealCosts)
		if !s.Applicable {
			s.SkippedReason = fmt.Sprintf("нет данных: в арендном кластере нет объявлений с явной мебелью (не указана у %d из %d)", unknown, stats.N)
		}
		rep.Scenarios = append(rep.Scenarios, s)
	} else {
		// Мебели нет (или сомнение): оба сценария (issue п.3–4).
		unf := ComputeScenario("без мебели", unfMed, unfP.p25, unfP.p75, unfComps, price, 0, dealCosts)
		if !unf.Applicable {
			unf.SkippedReason = fmt.Sprintf("нет данных: в арендном кластере нет объявлений с явным «без мебели» (не указана у %d из %d)", unknown, stats.N)
		}
		furn := ComputeScenario("с мебелью (после меблировки)", furnMed, furnP.p25, furnP.p75, furnComps, price, e.Config.FurnishingCostRUB, dealCosts)
		if !furn.Applicable {
			furn.SkippedReason = fmt.Sprintf("нет данных: в арендном кластере нет объявлений с явной мебелью (не указана у %d из %d)", unknown, stats.N)
		}
		rep.Scenarios = append(rep.Scenarios, unf, furn)
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
