-- Объявления недвижимости с Авито (результат парсинга).
-- Специализированные колонки -- для часто фильтруемых полей,
-- params/raw (JSONB) -- полное сохранение всех полученных данных.
CREATE TABLE IF NOT EXISTS avito_listings (
    id              BIGINT PRIMARY KEY,           -- avito item id
    url             TEXT NOT NULL,                -- https://www.avito.ru{url_path}
    url_path        TEXT NOT NULL,                -- /moskva/kvartiry/1-k._kvartira_..._1234567890

    category        TEXT NOT NULL,                -- kvartiry | komnaty | doma_dachi_kottedzhi | zemelnye_uchastki | garazhi_i_mashinomesta | kommercheskaya_nedvizhimost
    deal_type       TEXT NOT NULL CHECK (deal_type IN ('sale', 'rent_long', 'rent_daily')),

    title           TEXT,
    description     TEXT,

    -- Цена
    price           BIGINT,                       -- значение в копейках не храним, только целые
    price_currency  TEXT DEFAULT 'RUB',
    price_per_unit  BIGINT,                       -- цена за м2 (или за сотку для участков)
    price_unit      TEXT,                         -- 'м2' | 'сотка' | ...
    price_meta      JSONB,                        -- priceDetailed целиком (снижение цены, ипотека и т.п.)

    -- Ключевые параметры недвижимости
    rooms           INTEGER,
    studio          BOOLEAN,
    total_area      NUMERIC(12, 3),               -- м2 (участки -- в сотках, см. params)
    living_area     NUMERIC(12, 3),
    kitchen_area    NUMERIC(12, 3),
    land_area       NUMERIC(14, 3),               -- сотки (участки/дома)
    floor           INTEGER,
    floors_total    INTEGER,
    house_type      TEXT,
    renovation      TEXT,
    balcony         TEXT,
    bathroom        TEXT,
    year_built      INTEGER,

    -- География
    address         TEXT,                         -- человекочитаемый адрес
    region          TEXT,
    city            TEXT,
    district        TEXT,
    metro           TEXT,
    lat             DOUBLE PRECISION,
    lng             DOUBLE PRECISION,
    geo             JSONB,                        -- исходный geo-объект

    -- Продавец
    seller_name     TEXT,
    seller_type     TEXT,                         -- private | agency | developer | company
    seller_url      TEXT,
    seller_rating   NUMERIC(4, 2),
    seller          JSONB,                        -- весь блок продавца

    -- Медиа
    images          JSONB,                        -- [{url, w, h}] -- лучшие доступные размеры
    image_count     INTEGER,

    -- Метрики объявления
    views_count     INTEGER,
    contacts_count  INTEGER,
    favorites_count INTEGER,
    published_at    TIMESTAMPTZ,                  -- дата публикации на Авито
    refreshed_at    TIMESTAMPTZ,                  -- обновление объявления продавцом

    -- Служебные
    params          JSONB NOT NULL DEFAULT '{}',  -- все параметры объявления [{title, value, ...}]
    raw             JSONB NOT NULL,               -- исходный JSON карточки (initialData / mobile api)
    source_task     TEXT,                         -- тег задачи парсинга
    first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_avito_listings_category_deal ON avito_listings (category, deal_type);
CREATE INDEX IF NOT EXISTS idx_avito_listings_price ON avito_listings (price) WHERE price IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_avito_listings_rooms ON avito_listings (rooms) WHERE rooms IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_avito_listings_geo ON avito_listings (lat, lng) WHERE lat IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_avito_listings_published ON avito_listings (published_at DESC);
CREATE INDEX IF NOT EXISTS idx_avito_listings_params ON avito_listings USING GIN (params);
CREATE INDEX IF NOT EXISTS idx_avito_listings_raw ON avito_listings USING GIN (raw);

-- Журнал запусков парсинга: что парсили, что нашли.
CREATE TABLE IF NOT EXISTS avito_parse_runs (
    id              BIGSERIAL PRIMARY KEY,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ,
    filters         JSONB NOT NULL,               -- поисковый запрос (структура SearchFilters)
    request_url     TEXT,                         -- итоговый URL первой страницы
    pages_fetched   INTEGER NOT NULL DEFAULT 0,
    items_found     INTEGER NOT NULL DEFAULT 0,
    items_new       INTEGER NOT NULL DEFAULT 0,
    items_updated   INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'running',  -- running | ok | partial | error
    error           TEXT
);
