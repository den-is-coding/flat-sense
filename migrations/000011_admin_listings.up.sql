-- Админская таблица объявлений (issue #57): колонка «название ЖК»
-- (раньше ЖК оставался только внутри params) и индексы под фильтры/сортировки.
ALTER TABLE avito_listings ADD COLUMN IF NOT EXISTS residential_complex TEXT;

-- Бэкфилл из params: массив [{title, value}], название ЖК встречается
-- в параметрах «ЖК», «Название ЖК», «Жилой комплекс».
UPDATE avito_listings SET residential_complex = sub.value
FROM (
    SELECT id,
           (SELECT p.value
            FROM jsonb_to_recordset(params) AS p(title text, value text)
            WHERE p.title ILIKE '%жк%' OR p.title ILIKE '%жилой комплекс%'
            LIMIT 1) AS value
    FROM avito_listings
    WHERE jsonb_typeof(params) = 'array'
) AS sub
WHERE avito_listings.id = sub.id
  AND sub.value IS NOT NULL
  AND avito_listings.residential_complex IS NULL;

CREATE INDEX IF NOT EXISTS idx_avito_listings_city ON avito_listings (city);
CREATE INDEX IF NOT EXISTS idx_avito_listings_complex
    ON avito_listings (residential_complex) WHERE residential_complex IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_avito_listings_first_seen ON avito_listings (first_seen_at DESC);
