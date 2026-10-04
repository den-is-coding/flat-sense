package mapview

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Thresholds — пороги цветового кодирования по доходности (годовые %):
// ≥ GreenMin — зелёный, ≥ YellowMin — жёлтый, ниже — красный; нет данных —
// серый. Задаются окружением, не хардкодом (issue #73, доработка 2).
type Thresholds struct {
	GreenMin  float64 `json:"greenMin"`
	YellowMin float64 `json:"yellowMin"`
}

func thresholdsFromEnv() Thresholds {
	t := Thresholds{GreenMin: 8, YellowMin: 5}
	if v, err := strconv.ParseFloat(os.Getenv("YIELD_GREEN_MIN"), 64); err == nil && v > 0 {
		t.GreenMin = v
	}
	if v, err := strconv.ParseFloat(os.Getenv("YIELD_YELLOW_MIN"), 64); err == nil && v > 0 && v < t.GreenMin {
		t.YellowMin = v
	}
	return t
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
<style>
 body{margin:0;font:14px/1.45 system-ui,sans-serif;color:#1a1a1a}
 header{padding:10px 16px;background:#232a35;color:#fff}
 header h1{font-size:17px;margin:0}
 header .intro{color:#9aa4b2;font-size:13px;margin-top:2px}
 #map{height:72vh;min-height:420px;position:relative}
 .metric-panel{position:absolute;top:10px;right:10px;z-index:1000;background:#fff;
   border:1px solid #ddd;border-radius:6px;padding:6px;box-shadow:0 1px 4px rgba(0,0,0,.2)}
 .metric-panel button{display:block;width:100%;margin:2px 0;padding:6px 10px;border:1px solid #ccc;
   background:#fff;cursor:pointer;border-radius:4px;text-align:left}
 .metric-panel button.active{background:#232a35;color:#fff;border-color:#232a35}
 .filters{display:flex;flex-wrap:wrap;gap:8px;align-items:center;padding:8px 16px;background:#f5f6f8;border-bottom:1px solid #ddd}
 .filters input,.filters select{padding:4px 6px}
 .card{position:absolute;top:10px;bottom:10px;right:10px;width:320px;max-width:calc(100% - 20px);
   background:#fff;border:1px solid #ddd;border-radius:8px;box-shadow:0 2px 12px rgba(0,0,0,.25);
   z-index:1100;padding:12px;overflow-y:auto;display:none;box-sizing:border-box}
 .card .close{float:right;border:none;background:none;font-size:18px;cursor:pointer}
 .card img{width:100%;border-radius:6px;margin-bottom:8px}
 .card .muted{color:#666}
 .legend{position:absolute;bottom:10px;right:10px;z-index:1000;background:#fff;border:1px solid #ddd;
   border-radius:6px;padding:6px 10px;font-size:13px;box-shadow:0 1px 4px rgba(0,0,0,.2)}
 .legend .head{cursor:pointer;user-select:none}
 .legend .body{margin-top:4px}
 .dot{display:inline-block;width:10px;height:10px;border-radius:50%;margin-right:6px}
 .dot.green{background:#1a7f37}.dot.yellow{background:#e3a008}.dot.red{background:#c62828}.dot.gray{background:#9e9e9e}
 .pin{display:flex;align-items:center;justify-content:center;border-radius:13px;border:2px solid #fff;
   box-shadow:0 1px 4px rgba(0,0,0,.4);font-size:11px;font-weight:600;color:#fff;white-space:nowrap}
 .pin.green{background:#1a7f37}.pin.yellow{background:#e3a008}.pin.red{background:#c62828}.pin.gray{background:#9e9e9e}
 .cluster-pin{display:flex;flex-direction:column;align-items:center;justify-content:center;border-radius:50%;
   border:3px solid #fff;box-shadow:0 2px 6px rgba(0,0,0,.4);color:#fff;font-weight:700;box-sizing:border-box}
 .cluster-pin .n{font-size:14px;line-height:1.1}
 .cluster-pin .v{font-size:10px;font-weight:500;opacity:.95}
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
</div>
<div id="map">
 <div class="metric-panel" id="metric-panel">
  <button data-metric="price">Цена</button>
  <button data-metric="cost">Полная стоимость</button>
  <button data-metric="yield">Доходность с мебелью</button>
 </div>
 <div class="legend" id="legend">
  <div class="head" id="legend-head">Доходность, % годовых ▾</div>
  <div class="body" id="legend-body">
   <div><span class="dot green"></span>≥ <span id="lg-green"></span> %</div>
   <div><span class="dot yellow"></span><span id="lg-yellow"></span>–<span id="lg-green2"></span> %</div>
   <div><span class="dot red"></span>&lt; <span id="lg-yellow2"></span> %</div>
   <div><span class="dot gray"></span>нет данных</div>
  </div>
 </div>
 <div class="card" id="card"></div>
</div>
<script>window.MAP_THRESHOLDS = {{.Thresholds}};</script>
<script src="https://unpkg.com/leaflet@1.9.4/dist/leaflet.js"></script>
<script src="https://unpkg.com/leaflet.markercluster@1.5.3/dist/leaflet.markercluster.js"></script>
<script>
{{template "mapjs" .}}
</script>
</body>
</html>
`
