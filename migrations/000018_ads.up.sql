-- Проекция объявлений для чтения (issue #5): ads-service консьюмит
-- parsed-ads и держит собственную read-модель — срез avito_listings,
-- достаточный карточке объекта и поиску похожих. Владелец исходных
-- данных — parser-service (avito_listings, миграция 000010/000011);
-- эта таблица — проекция ads-service, upsert по avito_id.
-- Поля анализа (ROI и пр.) — НЕ здесь: analysis_results ниже,
-- полный расчёт живёт у analyzer-service (ad_roi_results, 000014).
CREATE TABLE IF NOT EXISTS ads (
    avito_id            BIGINT PRIMARY KEY,            -- avito_listings.id (item id Авито)
    url                 TEXT NOT NULL,                 -- https://www.avito.ru{url_path}
    url_path            TEXT NOT NULL DEFAULT '',      -- /moskva/kvartiry/..._1234567890
    title               TEXT NOT NULL DEFAULT '',
    deal_type           TEXT NOT NULL DEFAULT '',      -- sale | rent_long | rent_daily
    category            TEXT NOT NULL DEFAULT '',      -- kvartiry | komnaty | ...
    price               BIGINT,                        -- ₽; NULL = не распознано
    price_currency      TEXT NOT NULL DEFAULT 'RUB',
    rooms               INTEGER,                       -- NULL у студий/не распознано
    studio              BOOLEAN NOT NULL DEFAULT FALSE,
    total_area          DOUBLE PRECISION,              -- м2
    floor               INTEGER,
    floors_total        INTEGER,
    address             TEXT NOT NULL DEFAULT '',
    city                TEXT NOT NULL DEFAULT '',
    district            TEXT NOT NULL DEFAULT '',
    metro               TEXT NOT NULL DEFAULT '',
    residential_complex TEXT NOT NULL DEFAULT '',      -- название ЖК (для поиска похожих)
    lat                 DOUBLE PRECISION,
    lng                 DOUBLE PRECISION,
    photos              JSONB NOT NULL DEFAULT '[]',   -- [{url, width, height}]
    description         TEXT NOT NULL DEFAULT '',
    request_id          TEXT NOT NULL DEFAULT '',      -- request_id последнего parsed_ad по строке
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ads_residential_complex
    ON ads (residential_complex) WHERE residential_complex <> '';
CREATE INDEX IF NOT EXISTS idx_ads_created_at ON ads (created_at DESC);

-- Результат анализа объявления (issue #5): минимальное хранение для
-- AdsService.UpdateAnalysis/GetAd. Полный расчёт окупаемости — у
-- analyzer-service (ad_roi_results, миграция 000014); здесь — срез,
-- который ads-service отдаёт вместе с объявлением.
CREATE TABLE IF NOT EXISTS analysis_results (
    ad_id          BIGINT PRIMARY KEY REFERENCES ads(avito_id) ON DELETE CASCADE,
    status         TEXT NOT NULL DEFAULT '',      -- ok | no_rent_data (analyzer.v1.AnalysisResult.status)
    rent_forecast  DOUBLE PRECISION,              -- ожидаемая аренда, ₽/мес (медиана применимого сценария)
    yield_percent  DOUBLE PRECISION,              -- грубая валовая доходность, % годовых (rent*12/price)
    payback_years  DOUBLE PRECISION,              -- грубый срок окупаемости, лет (price/(rent*12))
    payload        JSONB NOT NULL DEFAULT '{}',   -- analyzer.v1.AnalysisResult целиком (protojson)
    computed_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
