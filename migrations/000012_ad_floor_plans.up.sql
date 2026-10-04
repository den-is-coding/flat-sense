-- Детекция планировок среди фото объявления (issue #58).
-- Каждое проверенное фото получает вердикт; «отрицательные» решения
-- тоже хранятся, чтобы повторная обработка не пересматривала фото.

CREATE TABLE IF NOT EXISTS ad_floor_plans (
    id           BIGSERIAL PRIMARY KEY,
    ad_id        BIGINT NOT NULL REFERENCES avito_listings(id) ON DELETE CASCADE,
    photo_ref    TEXT   NOT NULL,              -- URL фото или ключ в MinIO (задача #41)
    is_plan      BOOLEAN NOT NULL,             -- фото является планировкой
    confidence   REAL   NOT NULL,              -- 0..1
    method       TEXT   NOT NULL,              -- heuristic | ml | manual
    checked_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (ad_id, photo_ref)
);

-- Основная планировка объявления: максимальная confidence среди is_plan.
CREATE INDEX IF NOT EXISTS idx_ad_floor_plans_main
    ON ad_floor_plans (ad_id, confidence DESC) WHERE is_plan;
