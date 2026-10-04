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

// Thresholds — границы цветовых диапазонов по метрикам (issue #108:
// цвет следует выбранной метрике). N границ → N+1 диапазонов;
// «дешевле/выше = зеленее». Задаются окружением (списки через запятую),
// дефолты — пороги из макета #107 (легенды «Цена»/«Полная стоимость»).
type Thresholds struct {
	Yield []float64 `json:"yield"` // % годовых
	Price []float64 `json:"price"` // ₽
	Cost  []float64 `json:"cost"`  // ₽
}

// thresholdsFromEnv — границы всех трёх метрик из окружения.
func thresholdsFromEnv() Thresholds {
	return Thresholds{
		Yield: boundariesFromEnv("YIELD_THRESHOLDS", []float64{5, 8}),
		Price: boundariesFromEnv("PRICE_THRESHOLDS", []float64{7_500_000, 9_000_000}),
		Cost:  boundariesFromEnv("COST_THRESHOLDS", []float64{9_500_000, 11_500_000}),
	}
}

func boundariesFromEnv(key string, def []float64) []float64 {
	if v := os.Getenv(key); v != "" {
		var out []float64
		for _, s := range strings.Split(v, ",") {
			if x, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && x > 0 {
				out = append(out, x)
			}
		}
		if len(out) >= 1 {
			sort.Float64s(out)
			return out
		}
	}
	return def
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
// pageHTML — разметка страницы карты (#108): компактный топбар
// (бургер + бренд + профиль), панель фильтров, карта, дровер навигации;
// текстовой шапки и футера больше нет — атрибуция OSM/тайлов остаётся
// в контроле карты (лицензионное требование ODbL).
const pageHTML = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Карта студий Санкт-Петербурга — цена, полная стоимость, доходность</title>
<meta name="description" content="Интерактивная карта объявлений о студиях в Санкт-Петербурге: цена, полная стоимость и доходность с мебелью, кластеризация по районам.">
<link rel="stylesheet" href="https://unpkg.com/leaflet@1.9.4/dist/leaflet.css">
<link rel="stylesheet" href="https://unpkg.com/leaflet.markercluster@1.5.3/dist/MarkerCluster.css">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800&display=swap">
<style>
{{.TokensCSS}}
</style>
<style>
 body{margin:0;font:14px/1.45 var(--font-body),system-ui,sans-serif;color:var(--color-text-primary);
   background:var(--color-bg);display:flex;flex-direction:column;height:100vh;overflow:hidden}
 .topbar{display:flex;align-items:center;gap:10px;padding:8px 12px;background:var(--color-surface);
   border-bottom:1px solid var(--color-border);flex:none}
 .icon-btn{display:inline-flex;align-items:center;justify-content:center;width:40px;height:40px;
   border:1px solid var(--color-border);border-radius:var(--radius-sm);background:var(--color-surface);
   color:var(--color-text-primary);cursor:pointer;text-decoration:none}
 .icon-btn:hover{border-color:var(--color-accent);color:var(--color-accent-hover)}
 .icon-btn svg{width:20px;height:20px}
 .brand{display:flex;align-items:center;gap:8px;font-family:var(--font-heading);font-weight:700;
   font-size:16px;color:var(--color-text-primary);text-decoration:none}
 .logo-mark{display:inline-flex;align-items:center;justify-content:center;width:26px;height:26px;
   border-radius:var(--radius-sm);background:var(--color-accent);color:var(--color-accent-on);flex:none}
 .logo-mark svg{width:15px;height:15px}
 .topbar .spacer{flex:1}
 .filters{display:flex;flex-wrap:wrap;gap:8px;align-items:center;padding:8px 12px;
   background:var(--color-surface-2);border-bottom:1px solid var(--color-border);flex:none}
 .filters input[type=number],.filters select{padding:5px 6px;background:var(--color-input-bg);
   color:var(--color-text-primary);border:1px solid var(--color-border);border-radius:var(--radius-sm)}
 .filters .btn{padding:5px 10px;background:var(--color-surface);color:var(--color-text-primary);
   border:1px solid var(--color-border-strong);border-radius:var(--radius-sm);cursor:pointer}
 .filters .btn:hover{border-color:var(--color-accent);color:var(--color-accent-hover)}
 /* чекбокс по дизайн-коду (auth-flow.pen): скрытый input + стилизованная коробка */
 .check{position:relative;display:inline-flex;align-items:center;gap:8px;cursor:pointer;
   color:var(--color-text-secondary);user-select:none}
 .check input{position:absolute;opacity:0;width:0;height:0}
 .check .box{width:18px;height:18px;border:1.5px solid var(--color-border-strong);border-radius:6px;
   background:var(--color-input-bg);display:inline-flex;align-items:center;justify-content:center;
   flex:none;transition:background .15s,border-color .15s}
 .check .box svg{width:12px;height:12px;color:var(--color-accent-on);opacity:0;transition:opacity .15s}
 .check input:checked + .box{background:var(--color-accent);border-color:var(--color-accent)}
 .check input:checked + .box svg{opacity:1}
 .check:hover .box{border-color:var(--color-accent)}
 .check input:focus-visible + .box{outline:2px solid var(--color-accent-ring);outline-offset:1px}
 #map{flex:1 1 auto;min-height:320px;position:relative}
 .metric-panel{position:absolute;top:10px;right:10px;z-index:1000;background:var(--color-surface);
   border:1px solid var(--color-border);border-radius:var(--radius-md);padding:6px;
   box-shadow:0 1px 4px var(--map-shadow)}
 .metric-panel button{display:flex;align-items:center;gap:6px;width:100%;margin:2px 0;padding:6px 10px;
   border:1px solid var(--color-border);background:var(--color-surface);color:var(--color-text-primary);
   cursor:pointer;border-radius:var(--radius-sm);text-align:left}
 .metric-panel button svg{width:14px;height:14px;flex:none}
 .metric-panel button.active{background:var(--color-accent);color:var(--color-accent-on);border-color:var(--color-accent)}
 .metric-panel button.active svg{color:var(--color-accent-on)}
 .basemap-panel{position:absolute;top:10px;left:64px;z-index:1000;display:flex;gap:2px;
   background:var(--color-surface);border:1px solid var(--color-border);border-radius:var(--radius-md);
   padding:3px;box-shadow:0 1px 4px var(--map-shadow)}
 .basemap-panel button{padding:4px 8px;border:1px solid transparent;background:var(--color-surface);
   color:var(--color-text-secondary);cursor:pointer;border-radius:var(--radius-sm);font-size:12px}
 .basemap-panel button.active{background:var(--color-accent-bg);border-color:var(--color-accent-ring);
   color:var(--color-text-primary);font-weight:600}
 .legend{position:absolute;bottom:10px;right:10px;z-index:1000;background:var(--color-surface);
   border:1px solid var(--color-border);border-radius:var(--radius-md);padding:6px 10px;font-size:13px;
   color:var(--color-text-primary);box-shadow:0 1px 4px var(--map-shadow)}
 .legend .head{cursor:pointer;user-select:none}
 .legend .body{margin-top:4px}
 .legend .title{color:var(--color-text-secondary);font-size:12px;margin-bottom:2px}
 .legend .note{color:var(--color-text-muted);font-size:11px;margin-top:4px}
 .ldot{display:inline-block;width:10px;height:10px;border-radius:50%;margin-right:6px}
 /* классы диапазонов b0 (худший) … bN (лучший); 3 диапазона — danger/warning/success */
 .ldot.b0{background:var(--color-danger)}.ldot.b1{background:var(--color-warning)}
 .ldot.b2{background:var(--color-success)}
 .pin-wrap{transform:translate(-50%,-50%)}
 .pin{display:flex;align-items:center;justify-content:center;border-radius:17px;
   border:2px solid var(--map-pin-border);box-shadow:0 1px 4px var(--map-shadow);
   font-size:12px;font-weight:600;white-space:nowrap;color:var(--map-bucket-fg);
   min-width:44px;height:30px;padding:0 10px;box-sizing:border-box}
 .pin.b0{background:var(--color-danger)}.pin.b1{background:var(--color-warning)}
 .pin.b2{background:var(--color-success)}
 .pin.b3{background:var(--map-bucket-1)}.pin.b4{background:var(--map-bucket-2)}
 .pin.b5{background:var(--map-bucket-3)}.pin.b6{background:var(--map-bucket-4)}
 .pin.gray,.cluster-pin.gray,.ldot.gray{background:var(--map-bucket-gray)}
 [data-theme="dark"] .pin.b1,[data-theme="dark"] .pin.b4,
 [data-theme="dark"] .pin.b5{color:var(--map-bucket-fg-bright)}
 @media (prefers-color-scheme: dark){ :root:not([data-theme="light"]) .pin.b1,
   :root:not([data-theme="light"]) .pin.b4,:root:not([data-theme="light"]) .pin.b5{color:var(--map-bucket-fg-bright)} }
 .cluster-pin{display:flex;flex-direction:column;align-items:center;justify-content:center;border-radius:50%;
   border:3px solid var(--map-pin-border);box-shadow:0 2px 6px var(--map-shadow);
   color:var(--map-bucket-fg);font-weight:700;box-sizing:border-box}
 .cluster-pin .n{font-size:16px;line-height:1.15}
 .cluster-pin .v{font-size:12px;font-weight:600;opacity:.95;line-height:1.1;max-width:92%;overflow:hidden}
 .cluster-pin.b0{background:var(--color-danger)}.cluster-pin.b1{background:var(--color-warning)}
 .cluster-pin.b2{background:var(--color-success)}
 .cluster-pin.b3{background:var(--map-bucket-1)}.cluster-pin.b4{background:var(--map-bucket-2)}
 .cluster-pin.b5{background:var(--map-bucket-3)}.cluster-pin.b6{background:var(--map-bucket-4)}
 [data-theme="dark"] .cluster-pin.b1,[data-theme="dark"] .cluster-pin.b4,
 [data-theme="dark"] .cluster-pin.b5{color:var(--map-bucket-fg-bright)}
 @media (prefers-color-scheme: dark){ :root:not([data-theme="light"]) .cluster-pin.b1,
   :root:not([data-theme="light"]) .cluster-pin.b4,:root:not([data-theme="light"]) .cluster-pin.b5{color:var(--map-bucket-fg-bright)} }
 .card{position:absolute;top:10px;bottom:10px;right:10px;width:400px;max-width:calc(100% - 20px);
   background:var(--color-surface);border:1px solid var(--color-border);border-radius:var(--radius-lg);
   box-shadow:0 2px 12px var(--map-shadow-lg);z-index:1100;padding:14px;overflow-y:auto;display:none;box-sizing:border-box}
 .card .close{float:right;border:none;background:none;font-size:18px;cursor:pointer;color:var(--color-text-secondary)}
 .card .close:hover{color:var(--color-danger)}
 .card .close svg{width:16px;height:16px}
 /* фото — компактный баннер фиксированной высоты: карточка не скроллится
    даже при полном наборе данных (сценарии + расходы) */
 .card img.photo{width:100%;height:150px;object-fit:cover;object-position:center;
   display:block;border-radius:var(--radius-md);margin-bottom:10px}
 .card .price-row{display:flex;align-items:baseline;gap:8px;flex-wrap:wrap}
 .card .price{font-family:var(--font-heading);font-weight:800;font-size:22px}
 .card .jk-badge{font-size:11px;padding:2px 8px;border-radius:999px;background:var(--color-accent-bg);
   color:var(--color-accent-hover);font-weight:600}
 .card .addr{color:var(--color-text-secondary);font-size:12px;margin-top:4px}
 .card hr{border:none;border-top:1px solid var(--color-border);margin:10px 0}
 .card .muted{color:var(--color-text-secondary)}
 /* ключевой блок: ожидаемая цена сдачи (макет ObjectCard.KeyData) */
 .key-data{background:var(--color-accent-bg);border-radius:var(--radius-md);padding:10px 12px;margin:10px 0}
 .key-data .lbl{font-size:12px;color:var(--color-text-secondary)}
 .key-data .main{font-family:var(--font-heading);font-weight:800;font-size:30px;line-height:1.15;
   color:var(--color-text-primary)}
 .key-data .range-row{margin-top:2px;font-size:14px}
 .key-data .range{color:var(--color-accent-hover);font-weight:600}
 .key-data .range-n{color:var(--color-text-muted);font-size:12px}
 .scenarios{display:flex;flex-direction:column;gap:4px;font-size:13px;color:var(--color-text-secondary);margin:8px 0}
 .scenarios .row{display:flex;align-items:center;gap:6px}
 .scenarios .sdot{width:8px;height:8px;border-radius:50%;flex:none}
 .scenarios .s-furn .sdot{background:var(--color-success)}
 .scenarios .s-unfurn .sdot{background:var(--color-warning)}
 .key-data .key-metrics{margin-top:4px;font-size:13px;color:var(--color-text-secondary)}
 .key-data .key-metrics b{color:var(--color-text-primary)}
 /* расшифровка расходов сделки (#108) */
 .costs{margin:10px 0;border:1px solid var(--color-border);border-radius:var(--radius-md);padding:8px 12px}
 .costs-head{font-size:12px;color:var(--color-text-secondary);margin-bottom:4px}
 .costs .row{display:flex;justify-content:space-between;gap:12px;padding:2px 0;font-size:13px;color:var(--color-text-secondary)}
 .costs .row b{color:var(--color-text-primary);font-variant-numeric:tabular-nums;white-space:nowrap}
 .costs .row.total{border-top:1px solid var(--color-border);margin-top:4px;padding-top:6px;
   color:var(--color-text-primary);font-weight:600}
 .card a.avito{display:inline-block;margin-top:8px;color:var(--color-accent-hover);font-weight:600}
 .drawer-overlay{position:fixed;inset:0;background:var(--color-overlay);opacity:0;pointer-events:none;
   transition:opacity .2s;z-index:1300}
 .drawer-overlay.open{opacity:1;pointer-events:auto}
 .drawer{position:fixed;top:0;bottom:0;left:0;width:290px;max-width:85vw;background:var(--color-surface);
   border-right:1px solid var(--color-border);z-index:1400;transform:translateX(-102%);
   transition:transform .2s;display:flex;flex-direction:column;padding:14px;box-sizing:border-box}
 .drawer.open{transform:none}
 .drawer .drawer-brand{display:flex;align-items:center;gap:8px;font-family:var(--font-heading);
   font-weight:700;font-size:17px;margin-bottom:14px}
 .drawer nav{display:flex;flex-direction:column;gap:2px}
 .drawer nav a{display:flex;align-items:center;gap:10px;padding:10px 12px;border-radius:var(--radius-sm);
   color:var(--color-text-primary);text-decoration:none;font-weight:500}
 .drawer nav a svg{width:18px;height:18px;color:var(--color-text-secondary)}
 .drawer nav a:hover{background:var(--color-surface-2)}
 .drawer nav a.active{background:var(--color-accent-bg);color:var(--color-accent-hover);font-weight:600}
 .drawer nav a.active svg{color:var(--color-accent)}
 .drawer .drawer-foot{margin-top:auto;border-top:1px solid var(--color-border);padding-top:10px}
 .drawer .drawer-foot a{display:flex;align-items:center;gap:10px;padding:10px 12px;border-radius:var(--radius-sm);
   color:var(--color-text-primary);text-decoration:none;font-weight:500}
 .drawer .drawer-foot a svg{width:18px;height:18px;color:var(--color-text-secondary)}
 .drawer .drawer-foot a:hover{background:var(--color-surface-2)}
 .leaflet-control-zoom a{width:44px !important;height:44px !important;line-height:44px !important;font-size:20px !important}
 @media (max-width:640px){ .legend{display:none} .card{left:10px;width:auto;top:auto;height:55vh}
   .basemap-panel{top:8px;left:56px} }
</style>
</head>
<body>
<header class="topbar">
 <button class="icon-btn" id="burger" aria-label="Разделы" title="Разделы"><i data-lucide="menu"></i></button>
 <a class="brand" href="/map"><span class="logo-mark"><i data-lucide="building-2"></i></span>ИнвестКвартал</a>
 <span class="spacer"></span>
 <a class="icon-btn" href="/admin/login" title="Профиль — вход" aria-label="Профиль"><i data-lucide="user-round"></i></a>
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
 <label class="check"><input id="f-hasroi" type="checkbox"><span class="box"><i data-lucide="check"></i></span>только с рентабельностью</label>
 <button class="btn" id="f-reset" type="button">Сброс</button>
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
  <div class="head" id="legend-head"></div>
  <div class="body" id="legend-body"></div>
 </div>
 <div class="card" id="card"></div>
</div>
<div class="drawer-overlay" id="drawer-overlay"></div>
<nav class="drawer" id="drawer" aria-label="Разделы">
 <div class="drawer-brand"><span class="logo-mark"><i data-lucide="building-2"></i></span>ИнвестКвартал</div>
 <nav id="drawer-nav"></nav>
 <div class="drawer-foot">
  <a href="/admin/login"><i data-lucide="user-round"></i>Войти в профиль</a>
 </div>
</nav>
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
