// Package backfill — пакетный пересчёт окупаемости объявлений-продаж
// (issue #66) модулем оценки #64 с записью в кэш ad_roi_results
// (миграция 000014). Идемпотентен: upsert обновляет расчёт, дублей
// не создаёт. Админ-таблица только читает кэш — формула в одном месте.
package backfill

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/zhk"
)

// Source — данные для backfill: продажи (вход оценки) и арендный пул.
type Source interface {
	SaleListings(ctx context.Context) ([]evaluate.Listing, error)
	RentListings(ctx context.Context) ([]evaluate.Listing, error)
}

// Row — строка кэша ad_roi_results. NULL-сценарии («—» в таблице) —
// nil-поля, не нули.
type Row struct {
	AdID             int64
	Status           string // ok | no_rent_data
	InputFurnishing  string
	YieldUnfurnished *float64
	TotalCostUnfurn  *int64
	RentMedianUnfurn *float64
	RentP25Unfurn    *float64
	RentP75Unfurn    *float64
	CompsUnfurn      *int
	YieldFurnished   *float64
	TotalCostFurn    *int64
	RentMedianFurn   *float64
	RentP25Furn      *float64
	RentP75Furn      *float64
	CompsFurn        *int
	// раскладка расходов сделки (#108): риэлтор / титул+оформление /
	// меблировка — считает analyzer, карта только показывает
	FurnishingCost *int64
	RealtorFee     *int64
	DealCostsOther *int64
	DealCostsTotal *int64
	ClusterN       *int
	Confidence     string
	Notice         string
}

// ComplexStat — сводка по одному ЖК: сколько объявлений обеих сторон
// в БД и сколько посчитано (предусловие issue #66 — counts в отчёте).
type ComplexStat struct {
	Name       string `json:"name"`
	Sales      int    `json:"sales"`
	Rents      int    `json:"rents"`
	Computed   int    `json:"computed"`
	NoRentData int    `json:"noRentData"`
	Skipped    int    `json:"skipped"` // ошибки расчёта (например, нет цены)
}

// Report — итог прогона.
type Report struct {
	Complexes  []ComplexStat `json:"complexes"`
	Other      ComplexStat   `json:"other"`    // объявления вне реестра ЖК
	Computed   int           `json:"computed"` // строк в ad_roi_results
	NoRentData int           `json:"noRentData"`
	Errors     []string      `json:"errors,omitempty"`
}

// RowFromReport — маппинг отчёта #64 в строку кэша (чистая функция).
// Сценарий «с мебелью» для уже меблированного объявления не содержит
// надбавки — полная стоимость в обоих случаях берётся из PriceUsed отчёта.
// cfg нужен для раскладки издержек: риэлтор = % от цены (issue #108),
// остальное (титул + оформление) — разность с общими издержками сделки.
func RowFromReport(rep *evaluate.Report, cfg evaluate.Config) Row {
	row := Row{
		Status:          rep.Status,
		InputFurnishing: string(rep.InputFurnishing),
		Confidence:      rep.Confidence,
		Notice:          rep.Notice,
	}
	if rep.Status != "ok" || rep.Cluster == nil {
		return row
	}
	n := rep.Cluster.N
	row.ClusterN = &n
	if rep.Listing != nil && rep.Listing.Price > 0 {
		realtor := int64(math.Round(float64(rep.Listing.Price) * cfg.RealtorFeePct / 100))
		var dealTotal int64
		for i := range rep.Scenarios {
			if rep.Scenarios[i].Applicable {
				dealTotal = rep.Scenarios[i].DealCosts
				if row.FurnishingCost == nil || rep.Scenarios[i].FurnishingCost > *row.FurnishingCost {
					fc := rep.Scenarios[i].FurnishingCost
					row.FurnishingCost = &fc
				}
			}
		}
		if dealTotal > 0 {
			row.RealtorFee = &realtor
			other := dealTotal - realtor
			row.DealCostsOther = &other
			row.DealCostsTotal = &dealTotal
		}
	}
	for i := range rep.Scenarios {
		s := &rep.Scenarios[i]
		if !s.Applicable {
			// причина «—» — в тултип ячейки (SkippedReason отдаёт модуль #64)
			if s.SkippedReason != "" {
				if row.Notice != "" {
					row.Notice += "; "
				}
				row.Notice += s.Name + ": " + s.SkippedReason
			}
			continue // неприменимый сценарий → «—», не ноль
		}
		switch {
		case s.Name == "без мебели":
			row.YieldUnfurnished = &s.YieldPct
			row.TotalCostUnfurn = &s.PriceUsed
			row.RentMedianUnfurn = &s.RentMedian
			row.RentP25Unfurn = &s.RentP25
			row.RentP75Unfurn = &s.RentP75
			row.CompsUnfurn = &s.Comps
		case row.YieldFurnished == nil && (s.Name == "с мебелью" || s.Name == "с мебелью (после меблировки)"):
			row.YieldFurnished = &s.YieldPct
			row.TotalCostFurn = &s.PriceUsed
			row.RentMedianFurn = &s.RentMedian
			row.RentP25Furn = &s.RentP25
			row.RentP75Furn = &s.RentP75
			row.CompsFurn = &s.Comps
		}
	}
	return row
}

// Runner — расчёт и запись кэша.
type Runner struct {
	Source Source
	Pool   *pgxpool.Pool
	Config evaluate.Config
}

// Run — полный прогон: продажи + арендный пул → оценка каждого
// объявления-продажи → upsert ad_roi_results. Продажи без цены
// пропускаются с записью в отчёт.
func (r *Runner) Run(ctx context.Context) (*Report, error) {
	sales, err := r.Source.SaleListings(ctx)
	if err != nil {
		return nil, fmt.Errorf("sale listings: %w", err)
	}
	rents, err := r.Source.RentListings(ctx)
	if err != nil {
		return nil, fmt.Errorf("rent listings: %w", err)
	}

	rep := &Report{Other: ComplexStat{Name: "ЖК не распознан"}}
	statFor := func(c *zhk.Complex) *ComplexStat {
		if c == nil {
			return &rep.Other
		}
		for i := range rep.Complexes {
			if rep.Complexes[i].Name == c.Name {
				return &rep.Complexes[i]
			}
		}
		rep.Complexes = append(rep.Complexes, ComplexStat{Name: c.Name})
		return &rep.Complexes[len(rep.Complexes)-1]
	}

	// Алиасы ЖК для обеих сторон (совпадение — по любому корпусу ЖК).
	for i := range sales {
		statFor(zhk.Enrich(&sales[i])).Sales++
	}
	for i := range rents {
		statFor(zhk.Enrich(&rents[i])).Rents++
	}

	// Оценка в памяти: арендный пул грузится один раз (оценщик больше
	// в источник за пулом не ходит).
	src := &poolSource{byID: map[int64]*evaluate.Listing{}, rents: rents}
	for i := range sales {
		src.byID[sales[i].ID] = &sales[i]
	}
	ev := evaluate.NewEvaluator(src, r.Config)

	for i := range sales {
		in := &sales[i]
		s := statFor(zhk.Match(in))
		if in.Price <= 0 {
			s.Skipped++
			rep.Errors = append(rep.Errors, fmt.Sprintf("объявление %d: нет цены — пропущено", in.ID))
			continue
		}
		out, err := ev.EvaluateByID(ctx, in.ID)
		if err != nil {
			s.Skipped++
			rep.Errors = append(rep.Errors, fmt.Sprintf("объявление %d: %v", in.ID, err))
			continue
		}
		row := RowFromReport(out, r.Config)
		row.AdID = in.ID
		if err := r.upsert(ctx, row); err != nil {
			return nil, fmt.Errorf("upsert %d: %w", in.ID, err)
		}
		switch row.Status {
		case "ok":
			s.Computed++
			rep.Computed++
		default:
			s.NoRentData++
			rep.NoRentData++
		}
	}

	orderStats(rep)
	return rep, nil
}

// Upsert — публичная запись одной строки кэша для потокового режима
// (#6): консьюмер parsed-ads и gRPC UpdateAnalysis кладут результат тем
// же идемпотентным upsert, что и пакетный прогон.
func (r *Runner) Upsert(ctx context.Context, row Row) error {
	return r.upsert(ctx, row)
}

// textOrNil — пустая строка → NULL (CHECK-констрейнты на confidence/
// input_furnishing допускают только NULL или значения из списка).
func textOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// upsert — идемпотентная запись: повторный прогон обновляет расчёт.
func (r *Runner) upsert(ctx context.Context, row Row) error {
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO ad_roi_results (
			ad_id, status, input_furnishing,
			yield_unfurnished_pct, total_cost_unfurnished, rent_median_unfurnished, rent_p25_unfurnished, rent_p75_unfurnished, comps_unfurnished,
			yield_furnished_pct, total_cost_furnished, rent_median_furnished, rent_p25_furnished, rent_p75_furnished, comps_furnished,
			furnishing_cost, realtor_fee, deal_costs_other, deal_costs_total,
			cluster_n, confidence, notice, computed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22, now())
		ON CONFLICT (ad_id) DO UPDATE SET
			status = EXCLUDED.status,
			input_furnishing = EXCLUDED.input_furnishing,
			yield_unfurnished_pct = EXCLUDED.yield_unfurnished_pct,
			total_cost_unfurnished = EXCLUDED.total_cost_unfurnished,
			rent_median_unfurnished = EXCLUDED.rent_median_unfurnished,
			rent_p25_unfurnished = EXCLUDED.rent_p25_unfurnished,
			rent_p75_unfurnished = EXCLUDED.rent_p75_unfurnished,
			comps_unfurnished = EXCLUDED.comps_unfurnished,
			yield_furnished_pct = EXCLUDED.yield_furnished_pct,
			total_cost_furnished = EXCLUDED.total_cost_furnished,
			rent_median_furnished = EXCLUDED.rent_median_furnished,
			rent_p25_furnished = EXCLUDED.rent_p25_furnished,
			rent_p75_furnished = EXCLUDED.rent_p75_furnished,
			comps_furnished = EXCLUDED.comps_furnished,
			furnishing_cost = EXCLUDED.furnishing_cost,
			realtor_fee = EXCLUDED.realtor_fee,
			deal_costs_other = EXCLUDED.deal_costs_other,
			deal_costs_total = EXCLUDED.deal_costs_total,
			cluster_n = EXCLUDED.cluster_n,
			confidence = EXCLUDED.confidence,
			notice = EXCLUDED.notice,
			computed_at = now()`,
		row.AdID, row.Status, textOrNil(row.InputFurnishing),
		row.YieldUnfurnished, row.TotalCostUnfurn, row.RentMedianUnfurn, row.RentP25Unfurn, row.RentP75Unfurn, row.CompsUnfurn,
		row.YieldFurnished, row.TotalCostFurn, row.RentMedianFurn, row.RentP25Furn, row.RentP75Furn, row.CompsFurn,
		row.FurnishingCost, row.RealtorFee, row.DealCostsOther, row.DealCostsTotal,
		row.ClusterN, textOrNil(row.Confidence), row.Notice)
	return err
}

// orderStats — ЖК в порядке реестра (целевые четыре — первыми),
// затем остальные по имени.
func orderStats(rep *Report) {
	byName := map[string]*ComplexStat{}
	for i := range rep.Complexes {
		byName[rep.Complexes[i].Name] = &rep.Complexes[i]
	}
	out := make([]ComplexStat, 0, len(rep.Complexes))
	for _, c := range zhk.Registry {
		if s, ok := byName[c.Name]; ok {
			out = append(out, *s)
			delete(byName, c.Name)
		}
	}
	rest := make([]string, 0, len(byName))
	for n := range byName {
		rest = append(rest, n)
	}
	sort.Strings(rest)
	for _, n := range rest {
		out = append(out, *byName[n])
	}
	rep.Complexes = out
}

// poolSource — evaluate.Source поверх уже загруженных срезов.
type poolSource struct {
	byID  map[int64]*evaluate.Listing
	rents []evaluate.Listing
}

func (s *poolSource) ListingByID(_ context.Context, id int64) (*evaluate.Listing, error) {
	l, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("listing %d not found", id)
	}
	return l, nil
}

func (s *poolSource) RentListings(_ context.Context) ([]evaluate.Listing, error) {
	return s.rents, nil
}
