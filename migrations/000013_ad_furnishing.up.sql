-- Метки меблировки объявлений (issue #64): результат эвристики по
-- описанию/признакам. Хранятся и «неопределённые», чтобы не пересматривать.
CREATE TABLE IF NOT EXISTS ad_furnishing (
    ad_id        BIGINT PRIMARY KEY REFERENCES avito_listings(id) ON DELETE CASCADE,
    furnishing   TEXT   NOT NULL CHECK (furnishing IN ('furnished', 'unfurnished', 'unknown')),
    confident    BOOLEAN NOT NULL,               -- определено ли явно (иначе консервативное допущение)
    evidence     TEXT,                            -- фраза/признак, по которому решено
    labeled_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ad_furnishing_furnishing ON ad_furnishing (furnishing);
