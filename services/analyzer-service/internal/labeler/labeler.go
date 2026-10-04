// Package labeler — простановка меток меблировки объявлениям из БД
// (таблица ad_furnishing, миграция 000013; issue #64).
package labeler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// Summary — итог разметки.
type Summary struct {
	Labeled     int `json:"labeled"`
	Furnished   int `json:"furnished"`
	Unfurnished int `json:"unfurnished"`
	Unknown     int `json:"unknown"`
}

// ByDealType — разбивка меток по типу сделки.
type ByDealType struct {
	DealType    string `json:"dealType"`
	Furnished   int    `json:"furnished"`
	Unfurnished int    `json:"unfurnished"`
	Unknown     int    `json:"unknown"`
}

// LabelAll размечает все объявления avito_listings (идемпотентно:
// метка обновляется при повторном прогоне). Возвращает итог и разбивку
// по типам сделки.
func LabelAll(ctx context.Context, pool *pgxpool.Pool) (*Summary, []ByDealType, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, deal_type, coalesce(description, ''), coalesce(params, '{}'::jsonb)
		FROM avito_listings`)
	if err != nil {
		return nil, nil, fmt.Errorf("select listings: %w", err)
	}
	defer rows.Close()

	sum := &Summary{}
	byType := map[string]*ByDealType{}
	type pending struct {
		id         int64
		furnishing string
		confident  bool
		evidence   string
	}
	var batch []pending
	for rows.Next() {
		var id int64
		var dealType string
		var desc string
		var paramsRaw []byte
		if err := rows.Scan(&id, &dealType, &desc, &paramsRaw); err != nil {
			return nil, nil, err
		}
		var params evaluate.ParamList
		if len(paramsRaw) > 0 {
			_ = json.Unmarshal(paramsRaw, &params) // пустой объект {} → nil
		}
		f, confident, evidence := evaluate.DetectFurnishingDetailed(desc, params)
		sum.Labeled++
		bt := byType[dealType]
		if bt == nil {
			bt = &ByDealType{DealType: dealType}
			byType[dealType] = bt
		}
		switch f {
		case evaluate.Furnished:
			sum.Furnished++
			bt.Furnished++
		case evaluate.Unfurnished:
			sum.Unfurnished++
			bt.Unfurnished++
		default:
			sum.Unknown++
			bt.Unknown++
		}
		batch = append(batch, pending{id, string(f), confident, evidence})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	for _, p := range batch {
		if _, err := pool.Exec(ctx, `
			INSERT INTO ad_furnishing (ad_id, furnishing, confident, evidence)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (ad_id) DO UPDATE SET
				furnishing = EXCLUDED.furnishing,
				confident = EXCLUDED.confident,
				evidence = EXCLUDED.evidence,
				labeled_at = now()`,
			p.id, p.furnishing, p.confident, p.evidence); err != nil {
			return nil, nil, fmt.Errorf("upsert label %d: %w", p.id, err)
		}
	}

	out := make([]ByDealType, 0, len(byType))
	for _, t := range []string{"rent_long", "rent_daily", "sale"} {
		if bt, ok := byType[t]; ok {
			out = append(out, *bt)
		}
	}
	return sum, out, nil
}
