-- Кэш результатов оценки окупаемости (issue #66, модуль #64): по строке
-- на объявление-продажу. Считает analyzer-service при backfill-прогоне
-- (-backfill-roi), админ-таблица только читает — формула в одном месте.
-- NULL в сценарных колонках = «—» в таблице (сценарий неприменим или нет
-- арендных данных), не ноль.
CREATE TABLE IF NOT EXISTS ad_roi_results (
    ad_id                     BIGINT PRIMARY KEY REFERENCES avito_listings(id) ON DELETE CASCADE,
    status                    TEXT NOT NULL CHECK (status IN ('ok', 'no_rent_data')),
    input_furnishing          TEXT CHECK (input_furnishing IN ('furnished', 'unfurnished', 'unknown')),

    -- сценарий «без мебели» (NULL — сценарий неприменим)
    yield_unfurnished_pct     DOUBLE PRECISION,
    total_cost_unfurnished    BIGINT,
    rent_median_unfurnished   DOUBLE PRECISION,
    comps_unfurnished         INTEGER,

    -- сценарий «с мебелью» (NULL — сценарий неприменим)
    yield_furnished_pct       DOUBLE PRECISION,
    total_cost_furnished      BIGINT,
    rent_median_furnished     DOUBLE PRECISION,
    comps_furnished           INTEGER,

    -- методика для подписи при наведении
    cluster_n                 INTEGER,
    confidence                TEXT CHECK (confidence IN ('high', 'medium', 'low')),
    notice                    TEXT,

    computed_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ad_roi_results_computed ON ad_roi_results (computed_at DESC);
