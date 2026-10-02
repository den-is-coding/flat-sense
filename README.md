# flat-sense

Запуск для разработки
docker build --target dev -t my-service:dev .
docker run -p 8080:8080 -v $(pwd):/app my-service:dev

Запуск для прода
docker build --target production -t my-service:latest .
docker run -p 8080:8080 my-service:latest

# ============================================================
# Парсер Авито (services/parser-service)
# ============================================================

Модуль парсинга объявлений недвижимости с avito.ru: продажа и аренда
(длительная/посуточная), все категории недвижимости, хранение в PostgreSQL
(таблица `avito_listings`, журнал прогонов `avito_parse_runs`).

## Как работает

1. **Фильтры → URL.** `internal/avito/filters.go` собирает URL выдачи
   (`https://www.avito.ru/{city}/{category}/{deal}?pmin=...&s=...`).
   Поддержаны классические параметры (цена, сортировка, частник/компания,
   страница, поисковая строка) и слаг-фильтр `f=` (площадь, этажи, тип дома,
   ремонт и т.д.) — слаги просто копируются из браузера, метод
   `FiltersFromURL` принимает готовый URL выдачи со всеми фильтрами.
2. **Обход защиты.** `internal/avito/client.go`: TLS-имперсонация Chrome
   (bogdanfinn/tls-client, профили 146/150/152 + согласованные заголовки),
   пул российских резидентных/мобильных прокси (свой cookie-jar на прокси),
   прогрев сессии главной страницей, паузы 2–6 с между запросами и длинные
   паузы каждые ~20 запросов, rotate-until-clean на 403/429/439 и капчу
   (охлаждение прокси, опциональная смена IP мобильного прокси, ротация).
3. **Извлечение данных.** Из HTML достаются встроенные JSON-состояния —
   современный `<script data-mfe-state="true">` и легаси
   `window.__initialData__`; парсер (`internal/avito/parse.go`) ищет поля
   по именам ключей, поэтому переживает мелкие изменения схемы Авито.
4. **Хранение.** `internal/avito/storage.go` upsert'ит объявления
   (`ON CONFLICT (id)`): специализированные колонки (цена, комнаты,
   площадь, этаж, тип дома, гео, продавец, счётчики) + полный снапшот
   в JSONB (`params`, `raw`, `geo`, `seller`, `images`).

## Запуск

```bash
# поднять инфраструктуру и применить миграции (модуль: 000010_avito_listings)
cd deploy && docker compose up -d postgres migrate

# разовый прогон по фильтрам (CLI)
go run ./cmd -task='{"city":"moskva","category":"kvartiry","deal":"prodam","priceMin":5000000,"priceMax":15000000,"sort":"104","sellerType":"1"}'

# разовый прогон по URL из браузера (все фильтры как на сайте)
go run ./cmd -url='https://www.avito.ru/moskva/kvartiry/prodam-ASgBAgICAUSSA~AQ4LNc?f=ASgBAgICA0TQgPl4xm...'

# разбор одной карточки
go run ./cmd -item='https://www.avito.ru/moskva/kvartiry/2-k_kvartira_54_m2_3221234567'
```

HTTP API сервиса (порт 8080):

| Метод | Путь        | Назначение |
|-------|-------------|-----------|
| POST  | `/api/parse`| Прогон парсинга. Тело: `{"filters": {...}}` или `{"url": "..."}`, опции `maxPages`, `fetchDetails`, `withListings`, `sourceTask` |
| GET   | `/api/item?url=...` | Разбор одной карточки + сохранение |
| GET   | `/api/runs?limit=20` | Журнал прогонов |
| GET   | `/health`   | Health-check |

## Важно про прокси

Авито защищён Qrator/Curator: датацентр-IP и зарубежные IP блокируются
практически сразу, со статического IP выдерживается ~1 запрос/30 с.
Рабочая схема — российские **мобильные 4G или резидентные** прокси
(задаются в `AVITO_PROXIES`), пауза между запросами 2–6 с, смена IP
только при блоке. Это свойство площадки, а не модуля: без живых RU-прокси
прогоны будут получать 429/капчу.