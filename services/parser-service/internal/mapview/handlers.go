package mapview

import (
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Thresholds — границы цветовых диапазонов доходности (годовые %),
// по возрастанию: N границ → N+1 диапазонов (7 границ по умолчанию).
// Диапазон i: [b[i-1], b[i]); первый — ниже b[0], последний — ≥ b[n-1].
// Нет данных — серый. Задаются окружением YIELD_THRESHOLDS (список через
// запятую), не хардкодом (issue #73, доработка 2; 7 диапазонов — по
// просьбе пользователя, границы подобраны по фактическому разбросу
// доходности, чтобы крайние диапазоны не пустовали).
type Thresholds struct {
	Boundaries []float64 `json:"boundaries"`
}

// defaultBoundaries — 6 границ → 7 диапазонов. Подобраны по фактическому
// разбросу доходности (4.0–5.8 %): все семь диапазонов непустые
// (1/2/2/4/1/12/1 объявлений на данных 2026-10-04).
func defaultBoundaries() []float64 {
	return []float64{4.1, 4.55, 4.75, 5.05, 5.35, 5.65}
}

func thresholdsFromEnv() Thresholds {
	b := defaultBoundaries()
	if v := os.Getenv("YIELD_THRESHOLDS"); v != "" {
		var out []float64
		for _, s := range strings.Split(v, ",") {
			if x, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && x > 0 {
				out = append(out, x)
			}
		}
		if len(out) >= 2 {
			sort.Float64s(out)
			b = out
		}
	}
	return Thresholds{Boundaries: b}
}

// Register вешает публичные маршруты карты: страница /map (индексируемая,
// без X-Robots-Tag noindex — в отличие от /admin) и bbox-API выдачи.
func Register(mux *http.ServeMux, store *Store) {
	t := thresholdsFromEnv()
	mux.HandleFunc("GET /map", func(w http.ResponseWriter, r *http.Request) {
		total, withCoords, err := store.Counts(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pageTmpl.Execute(w, pageData{
			Total:      total,
			WithCoords: withCoords,
			NoCoords:   total - withCoords,
			Thresholds: t,
			TokensCSS:  TokensCSS,
		})
	})
	mux.HandleFunc("GET /api/map/listings", func(w http.ResponseWriter, r *http.Request) {
		f, err := filtersFromRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, err := store.List(r.Context(), f)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		filtered := f.HasROI || f.YieldMin > 0 || f.PriceMax > 0 || f.Rooms != ""
		writeJSON(w, http.StatusOK, mapResponse{
			Items:      items,
			Filtered:   filtered,
			Thresholds: t,
		})
	})
}

// filtersFromRequest — bbox (minLng,minLat,maxLng,maxLat) и фильтры из
// query (применяются на бэкенде, до кластеризации на клиенте).
func filtersFromRequest(r *http.Request) (MapFilters, error) {
	q := r.URL.Query()
	f := MapFilters{
		HasROI: q.Get("has_roi") == "1",
		Rooms:  q.Get("rooms"),
	}
	if v := q.Get("yield_min"); v != "" {
		x, err := strconv.ParseFloat(v, 64)
		if err != nil || x < 0 {
			return f, errInvalidParam
		}
		f.YieldMin = x
	}
	if v := q.Get("price_max"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return f, errInvalidParam
		}
		f.PriceMax = n
	}
	// bbox ограничен СПб и окрестностями — защита от мусорных запросов.
	const (
		minLat, maxLat = 58.0, 61.0
		minLng, maxLng = 28.0, 33.0
	)
	parts := strings.Split(q.Get("bbox"), ",")
	if len(parts) != 4 {
		return f, errInvalidParam
	}
	nums := make([]float64, 4)
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return f, errInvalidParam
		}
		nums[i] = v
	}
	// порядок: minLng,minLat,maxLng,maxLat (перепутанные границы — не ошибка)
	if nums[0] > nums[2] {
		nums[0], nums[2] = nums[2], nums[0]
	}
	if nums[1] > nums[3] {
		nums[1], nums[3] = nums[3], nums[1]
	}
	if nums[1] < minLat || nums[3] > maxLat || nums[0] < minLng || nums[2] > maxLng {
		return f, errInvalidParam
	}
	f.BBox = [4]float64{nums[0], nums[1], nums[2], nums[3]}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			f.Limit = n
		}
	}
	return f, nil
}

type paramError struct{ msg string }

func (e paramError) Error() string { return e.msg }

var errInvalidParam = paramError{msg: "invalid parameters"}

// mapResponse — тело GET /api/map/listings.
type mapResponse struct {
	Items      []MapItem  `json:"items"`
	Filtered   bool       `json:"filtered"`
	Thresholds Thresholds `json:"thresholds"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// pageData — данные SSR-оболочки /map (индексируемые тексты).
type pageData struct {
	Total      int64
	WithCoords int64
	NoCoords   int64
	Thresholds Thresholds
	TokensCSS  template.CSS // доверенный CSS (html/template санитарит строки в <style>)
}

// pageHTML — разметка страницы карты (переключатель метрик, фильтры,
// легенда, карточка объекта; зум-кнопки — штатный Leaflet-контрол слева
// сверху, тапабельные 44px через CSS).
const pageHTML = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Карта студий Санкт-Петербурга — цена, полная стоимость, доходность</title>
<meta name="description" content="Интерактивная карта объявлений о студиях в Санкт-Петербурге на OpenStreetMap: цена, полная стоимость и доходность с мебелью, кластеризация по районам.">
<link rel="stylesheet" href="https://unpkg.com/leaflet@1.9.4/dist/leaflet.css">
<link rel="stylesheet" href="https://unpkg.com/leaflet.markercluster@1.5.3/dist/MarkerCluster.css">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Inter:wght@400;600;700&display=swap">
<style>
{{.TokensCSS}}
</style>
<style>
 body{margin:0;font:14px/1.45 var(--font-body),system-ui,sans-serif;color:var(--color-text-primary);
   background:var(--color-bg);display:flex;flex-direction:column;min-height:100vh}
 header{padding:10px 16px;background:var(--map-header-bg);color:var(--map-header-text)}
 header h1{font-size:17px;margin:0}
 header .intro{color:var(--map-header-muted);font-size:13px;margin-top:2px}
 #map{flex:1 1 auto;min-height:420px;position:relative}
 footer{padding:8px 16px;background:var(--color-surface-2);border-top:1px solid var(--color-border);
   color:var(--color-text-secondary);font-size:12px;display:flex;flex-wrap:wrap;gap:4px 18px}
 .metric-panel{position:absolute;top:10px;right:10px;z-index:1000;background:var(--color-surface);
   border:1px solid var(--color-border);border-radius:var(--radius-md);padding:6px;
   box-shadow:0 1px 4px var(--map-shadow)}
 .metric-panel button{display:flex;align-items:center;gap:6px;width:100%;margin:2px 0;padding:6px 10px;
   border:1px solid var(--color-border);background:var(--color-surface);color:var(--color-text-primary);
   cursor:pointer;border-radius:var(--radius-sm);text-align:left}
 .metric-panel button svg{width:14px;height:14px;flex:none}
 .metric-panel button.active{background:var(--color-accent);color:var(--color-accent-on);border-color:var(--color-accent)}
 .basemap-panel{position:absolute;top:10px;left:64px;z-index:1000;display:flex;gap:2px;
   background:var(--color-surface);border:1px solid var(--color-border);border-radius:var(--radius-md);
   padding:3px;box-shadow:0 1px 4px var(--map-shadow)}
 .basemap-panel button{padding:4px 8px;border:1px solid transparent;background:var(--color-surface);
   color:var(--color-text-secondary);cursor:pointer;border-radius:var(--radius-sm);font-size:12px}
 .basemap-panel button.active{background:var(--color-accent-bg);border-color:var(--color-accent-ring);
   color:var(--color-text-primary);font-weight:600}
 @media (max-width:640px){ .basemap-panel{top:8px;left:56px} }
 .filters{display:flex;flex-wrap:wrap;gap:8px;align-items:center;padding:8px 16px;
   background:var(--color-surface-2);border-bottom:1px solid var(--color-border)}
 .filters input,.filters select{padding:4px 6px;background:var(--color-input-bg);
   color:var(--color-text-primary);border:1px solid var(--color-border);border-radius:var(--radius-sm)}
 .filters button{padding:4px 10px;background:var(--color-surface);color:var(--color-text-primary);
   border:1px solid var(--color-border-strong);border-radius:var(--radius-sm);cursor:pointer}
 .filters button:hover{border-color:var(--color-accent);color:var(--color-accent-hover)}
 .filters label{color:var(--color-text-secondary)}
 #theme-toggle{display:inline-flex;align-items:center;gap:4px}
 #theme-toggle svg{width:14px;height:14px}
 .card{position:absolute;top:10px;bottom:10px;right:10px;width:320px;max-width:calc(100% - 20px);
   background:var(--color-surface);border:1px solid var(--color-border);border-radius:var(--radius-lg);
   box-shadow:0 2px 12px var(--map-shadow-lg);z-index:1100;padding:12px;overflow-y:auto;display:none;box-sizing:border-box}
 .card .close{float:right;border:none;background:none;font-size:18px;cursor:pointer;color:var(--color-text-secondary)}
 .card .close:hover{color:var(--color-danger)}
 .card .close svg{width:16px;height:16px}
 .card img{width:100%;border-radius:var(--radius-md);margin-bottom:8px}
 .card .muted{color:var(--color-text-secondary)}
 .card hr{border:none;border-top:1px solid var(--color-border)}
 .card a{color:var(--color-accent-hover)}
 .legend{position:absolute;bottom:10px;right:10px;z-index:1000;background:var(--color-surface);
   border:1px solid var(--color-border);border-radius:var(--radius-md);padding:6px 10px;font-size:13px;
   color:var(--color-text-primary);box-shadow:0 1px 4px var(--map-shadow)}
 .legend .head{cursor:pointer;user-select:none}
 .legend .body{margin-top:4px}
 .dot{display:inline-block;width:10px;height:10px;border-radius:50%;margin-right:6px}
 /* 7 диапазонов доходности (c0 — самый низкий, c6 — самый высокий) + серый;
    цвета — токены (dark-варианты перекрашиваются автоматически) */
 .dot.c0{background:var(--map-bucket-0)}.dot.c1{background:var(--map-bucket-1)}
 .dot.c2{background:var(--map-bucket-2)}.dot.c3{background:var(--map-bucket-3)}
 .dot.c4{background:var(--map-bucket-4)}.dot.c5{background:var(--map-bucket-5)}
 .dot.c6{background:var(--map-bucket-6)}.dot.gray{background:var(--map-bucket-gray)}
 .pin-wrap{transform:translate(-50%,-50%)} /* центровка метки любой ширины на точке */
 .pin{display:flex;align-items:center;justify-content:center;border-radius:17px;
   border:2px solid var(--map-pin-border);box-shadow:0 1px 4px var(--map-shadow);
   font-size:12px;font-weight:600;white-space:nowrap;color:var(--map-bucket-fg);
   min-width:44px;height:30px;padding:0 10px;box-sizing:border-box}
 .pin.c0{background:var(--map-bucket-0)}.pin.c1{background:var(--map-bucket-1)}
 .pin.c2{background:var(--map-bucket-2)}.pin.c3{background:var(--map-bucket-3)}
 .pin.c4{background:var(--map-bucket-4)}.pin.c5{background:var(--map-bucket-5)}
 .pin.c6{background:var(--map-bucket-6)}.pin.gray{background:var(--map-bucket-gray)}
 /* яркие диапазоны (c2, c3) в тёмной теме — тёмный текст */
 [data-theme="dark"] .pin.c2,[data-theme="dark"] .pin.c3{color:var(--map-bucket-fg-bright)}
 @media (prefers-color-scheme: dark){ :root:not([data-theme="light"]) .pin.c2,
   :root:not([data-theme="light"]) .pin.c3{color:var(--map-bucket-fg-bright)} }
 .cluster-pin{display:flex;flex-direction:column;align-items:center;justify-content:center;border-radius:50%;
   border:3px solid var(--map-pin-border);box-shadow:0 2px 6px var(--map-shadow);
   color:var(--map-bucket-fg);font-weight:700;box-sizing:border-box}
 /* фон кластера — сплошной, по диапазону максимума доходности внутри */
 .cluster-pin.c0{background:var(--map-bucket-0)}.cluster-pin.c1{background:var(--map-bucket-1)}
 .cluster-pin.c2{background:var(--map-bucket-2)}.cluster-pin.c3{background:var(--map-bucket-3)}
 .cluster-pin.c4{background:var(--map-bucket-4)}.cluster-pin.c5{background:var(--map-bucket-5)}
 .cluster-pin.c6{background:var(--map-bucket-6)}.cluster-pin.gray{background:var(--map-bucket-gray)}
 [data-theme="dark"] .cluster-pin.c2,[data-theme="dark"] .cluster-pin.c3{color:var(--map-bucket-fg-bright)}
 @media (prefers-color-scheme: dark){ :root:not([data-theme="light"]) .cluster-pin.c2,
   :root:not([data-theme="light"]) .cluster-pin.c3{color:var(--map-bucket-fg-bright)} }
 .cluster-pin .n{font-size:16px;line-height:1.15}
 .cluster-pin .v{font-size:12px;font-weight:600;opacity:.95;line-height:1.1;max-width:92%;overflow:hidden}
 .leaflet-control-zoom a{width:44px !important;height:44px !important;line-height:44px !important;font-size:20px !important}
 @media (max-width:640px){ .card{left:10px;width:auto;top:auto;height:55vh} .legend{display:none} }
</style>
</head>
<body>
<header>
 <h1>Карта студий Санкт-Петербурга</h1>
 <div class="intro">Объявлений всего: {{.Total}} · на карте с координатами: {{.WithCoords}} ·
 без координат не показано: {{.NoCoords}}. Метрики окупаемости рассчитаны по арендным аналогам
 того же ЖК. Карта и данные: © OpenStreetMap contributors.</div>
</header>
<div class="filters" id="filters">
 <label>доходность ≥ <input id="f-yield" type="number" step="0.5" min="0" style="width:70px"> %</label>
 <label>цена ≤ <input id="f-price" type="number" step="100000" min="0" style="width:110px"> ₽</label>
 <select id="f-rooms">
  <option value="">комнаты: любые</option>
  <option value="studio">студия</option>
  <option value="1">1</option>
  <option value="2">2</option>
  <option value="3plus">3+</option>
 </select>
 <label><input id="f-hasroi" type="checkbox"> только с рентабельностью</label>
 <button id="f-apply">Применить</button>
 <button id="f-reset">Сброс</button>
 <button id="theme-toggle" title="Тема: системная / светлая / тёмная" aria-label="Переключить тему"></button>
</div>
<div id="map">
 <div class="metric-panel" id="metric-panel">
  <button data-metric="price"><i data-lucide="banknote"></i>Цена</button>
  <button data-metric="cost"><i data-lucide="wallet"></i>Полная стоимость</button>
  <button data-metric="yield"><i data-lucide="percent"></i>Доходность</button>
 </div>
 <div class="basemap-panel" id="basemap-panel">
  <button data-basemap="osm" title="OpenStreetMap стандартная">ОСМ</button>
  <button data-basemap="light" title="Светлая (© OpenStreetMap contributors © CARTO)">Светлая</button>
  <button data-basemap="dark" title="Тёмная (© OpenStreetMap contributors © CARTO)">Тёмная</button>
 </div>
 <div class="legend" id="legend">
  <div class="head" id="legend-head">Доходность, % годовых ▾</div>
  <div class="body" id="legend-body"><!-- строки диапазонов строит map.js из MAP_THRESHOLDS --></div>
 </div>
 <div class="card" id="card"></div>
</div>
<footer>
 <span>flat-sense — данные объявлений: Авито</span>
 <span>метрики окупаемости рассчитаны по арендным аналогам того же ЖК</span>
 <span>Карта: © OpenStreetMap contributors</span>
</footer>
<script>window.MAP_THRESHOLDS = {{.Thresholds}};</script>
<script src="https://unpkg.com/leaflet@1.9.4/dist/leaflet.js"></script>
<script src="https://unpkg.com/leaflet.markercluster@1.5.3/dist/leaflet.markercluster.js"></script>
<script src="https://unpkg.com/lucide@0.469.0/dist/umd/lucide.min.js"></script>
<script>
{{template "mapjs" .}}
</script>
</body>
</html>
`
