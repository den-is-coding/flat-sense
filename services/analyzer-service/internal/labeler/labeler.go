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

// PhotoStats — итог фото-прохода.
type PhotoStats struct {
	Examined    int `json:"examined"` // объявлений с неоднозначным текстом и фото
	Labeled     int `json:"labeled"`  // фото-детектор дал вердикт
	Furnished   int `json:"furnished"`
	Unfurnished int `json:"unfurnished"`
	Unknown     int `json:"unknown"`
	Failed      int `json:"failed"` // image-ai недоступен / фото не скачались
}

// LabelPhotos — фото-фолбэк (issue #110): для объявлений, у которых текст
// не дал уверенной метки и есть фото, вызывает детектор и кэширует
// вердикт в ad_furnishing.furnished_photo*. Повторный прогон пропускает
// уже просмотренные (кэш не пересматривается).
func LabelPhotos(ctx context.Context, pool *pgxpool.Pool, det evaluate.PhotoFurnishingDetector) (*PhotoStats, error) {
	rows, err := pool.Query(ctx, `
		SELECT l.id, l.images
		FROM avito_listings l
		JOIN ad_furnishing f ON f.ad_id = l.id
		WHERE (f.furnishing = 'unknown' OR NOT f.confident)
		  AND f.furnished_photo IS NULL
		  AND jsonb_array_length(coalesce(l.images, '[]'::jsonb)) > 0`)
	if err != nil {
		return nil, fmt.Errorf("select unlabeled: %w", err)
	}
	defer rows.Close()
	type item struct {
		id   int64
		urls []string
	}
	var batch []item
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var imgs []struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(raw, &imgs)
		var urls []string
		for _, im := range imgs {
			if im.URL != "" {
				urls = append(urls, im.URL)
			}
		}
		if len(urls) > 0 {
			batch = append(batch, item{id: id, urls: urls})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	st := &PhotoStats{Examined: len(batch)}
	for _, it := range batch {
		f, conf, err := det.DetectByPhotos(ctx, it.urls)
		if err != nil {
			st.Failed++
			continue
		}
		st.Labeled++
		if _, err := pool.Exec(ctx, `
			UPDATE ad_furnishing
			SET furnished_photo = $2, furnished_photo_confidence = $3, furnished_photo_at = now()
			WHERE ad_id = $1`, it.id, string(f), conf); err != nil {
			return st, fmt.Errorf("update photo label %d: %w", it.id, err)
		}
		switch f {
		case evaluate.Furnished:
			st.Furnished++
		case evaluate.Unfurnished:
			st.Unfurnished++
		default:
			st.Unknown++
		}
	}
	return st, nil
}
