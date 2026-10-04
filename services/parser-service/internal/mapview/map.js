(function () {
  'use strict';
  // Карта объектов (issue #73): Leaflet + markercluster. Кластеризация —
  // markercluster, а не supercluster: точек сотни (не десятки тысяч),
  // из коробки клик-зум по кластеру и iconCreateFunction для кастомных
  // подписей (агрегат выбранной метрики) и цветов (агрегат доходности).

  // Метрики подписей: цена/полная стоимость → минимум в кластере,
  // доходность → максимум (правило агрегации issue #73).
  var METRICS = {
    price: {
      label: 'Цена', agg: 'min',
      value: function (d) { return d.price; },
      fmt: function (v) { return fmtRub(v); },
      fmtShort: function (v) { return fmtRubCompact(v); }
    },
    cost: {
      label: 'Полная стоимость', agg: 'min',
      // полная стоимость = с мебелью, при отсутствии — без мебели
      value: function (d) { return d.totalCostFurnished != null ? d.totalCostFurnished : d.totalCostUnfurnished; },
      fmt: function (v) { return fmtRub(v); },
      fmtShort: function (v) { return fmtRubCompact(v); }
    },
    yield: {
      label: 'Доходность', agg: 'max',
      value: function (d) { return d.yieldFurnished != null ? d.yieldFurnished : d.yieldUnfurnished; },
      fmt: function (v) { return v == null ? '—' : v.toFixed(1) + ' %'; },
      fmtShort: function (v) { return v == null ? '—' : v.toFixed(1) + ' %'; }
    }
  };
  var metric = localStorage.getItem('mapMetric') || 'price';
  if (!METRICS[metric]) metric = 'price';

  var map = L.map('map', { zoomControl: true }); // кнопки +/− слева сверху (доработка 1)
  window.__fsMap = map; // хук для e2e/отладки

  // Подложки (issue #73, по просьбе пользователя — переключатель стиля):
  // OSM стандартная, светлая и тёмная серые канвасы Esri (на данных OSM;
  // Carto-тайлы без API-ключа отдают заглушку «API KEY REQUIRED»).
  var BASEMAPS = {
    osm: {
      url: 'https://tile.openstreetmap.org/{z}/{x}/{y}.png',
      attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors'
    },
    light: {
      url: 'https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Light_Gray_Base/MapServer/tile/{z}/{y}/{x}',
      attribution: 'Tiles &copy; Esri — источник: Esri, HERE, Garmin, &copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors',
      opts: { maxNativeZoom: 16 }
    },
    dark: {
      url: 'https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Dark_Gray_Base/MapServer/tile/{z}/{y}/{x}',
      attribution: 'Tiles &copy; Esri — источник: Esri, HERE, Garmin, &copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors',
      opts: { maxNativeZoom: 16 }
    }
  };
  var basemap = localStorage.getItem('mapBasemap');
  if (!BASEMAPS[basemap]) basemap = 'osm';
  var tileLayer = makeTiles(basemap).addTo(map);
  map.fitBounds([[59.83, 30.15], [60.02, 30.45]]); // СПб по умолчанию

  function makeTiles(key) {
    var b = BASEMAPS[key];
    return L.tileLayer(b.url, Object.assign({ maxZoom: 19, attribution: b.attribution }, b.opts || {}));
  }

  var cluster = L.markerClusterGroup({
    showCoverageOnHover: false,
    maxClusterRadius: 60,
    iconCreateFunction: function (c) {
      var all = c.getAllChildMarkers();
      var n = all.length;
      var m = METRICS[metric];
      var agg = aggregate(all, m);
      var yMax = aggregateYield(all); // кластер красится по максимуму доходности
      // крупный круг: значение выбранной метрики должно помещаться
      // внутри с полями (компактный формат — «7,5 млн ₽»)
      var size = Math.min(92, 46 + n * 1.6);
      var html = '<div class="cluster-pin ' + colorClass(yMax) + '" style="width:' + size + 'px;height:' + size + 'px">' +
        '<span class="n">' + n + '</span><span class="v">' + m.fmtShort(agg) + '</span></div>';
      return L.divIcon({ html: html, className: '', iconSize: [size, size] });
    }
  });
  map.addLayer(cluster);

  function aggregate(markers, m) {
    var vals = [];
    for (var i = 0; i < markers.length; i++) {
      var v = m.value(markers[i].dataset);
      if (v != null) vals.push(v);
    }
    if (!vals.length) return null;
    return m.agg === 'max' ? Math.max.apply(null, vals) : Math.min.apply(null, vals);
  }

  function aggregateYield(markers) {
    var vals = [];
    for (var i = 0; i < markers.length; i++) {
      var d = markers[i].dataset;
      var v = d.yieldFurnished != null ? d.yieldFurnished : d.yieldUnfurnished;
      if (v != null) vals.push(v);
    }
    if (!vals.length) return null;
    return Math.max.apply(null, vals); // агрегат доходности кластера — максимум
  }

  // Диапазоны доходности: N границ → N+1 диапазонов, шкала красный→зелёный.
  // Индекс диапазона = число границ, которые значение перешагнуло.
  var B = (window.MAP_THRESHOLDS && window.MAP_THRESHOLDS.boundaries) || [4.1, 4.55, 4.75, 5.05, 5.35, 5.65];
  var BUCKET_COLORS = ['#b71c1c', '#d84315', '#ea7600', '#c79500', '#9e9d24', '#558b2f', '#1b5e20'];

  function bucketIndex(y) {
    if (y == null) return -1;
    var i = 0;
    while (i < B.length && y >= B[i]) i++;
    return i; // 0..B.length
  }

  function colorClass(y) {
    var i = bucketIndex(y);
    return i < 0 ? 'gray' : ('c' + i);
  }

  function fmtRub(v) {
    if (v == null) return '—';
    var s = Math.round(v).toString();
    var out = '';
    for (var i = 0; i < s.length; i++) {
      if (i > 0 && (s.length - i) % 3 === 0) out += '\u00a0';
      out += s[i];
    }
    return out + '\u00a0₽';
  }

  // компактный формат для кружков кластеров («7,5 млн ₽», «850 тыс ₽»),
  // чтобы значение помещалось внутри окружности с полями
  function fmtRubCompact(v) {
    if (v == null) return '—';
    if (v >= 1e6) return (v / 1e6).toFixed(1).replace('.', ',') + '\u00a0млн';
    if (v >= 1e3) return Math.round(v / 1e3) + '\u00a0тыс';
    return Math.round(v) + '\u00a0₽';
  }

  function pinIcon(d) {
    var y = d.yieldFurnished != null ? d.yieldFurnished : d.yieldUnfurnished;
    var m = METRICS[metric];
    var text = m.fmt(m.value(d));
    // обёртка с translate(-50%,-50%) центрирует метку любой ширины на точке
    return L.divIcon({
      className: '',
      html: '<div class="pin-wrap"><div class="pin ' + colorClass(y) + '">' + text + '</div></div>',
      iconSize: null
    });
  }

  function marker(d) {
    var mk = L.marker([d.lat, d.lng], { icon: pinIcon(d) });
    mk.dataset = d;
    mk.on('click', function () { showCard(d); });
    return mk;
  }

  // Карточка объекта (правая панель), закрытие по крестику/клику вне.
  var card = document.getElementById('card');
  function showCard(d) {
    var rows = '';
    if (d.area != null) rows += '<div>Площадь: ' + d.area + ' м²</div>';
    if (d.studio) rows += '<div>Студия</div>';
    else if (d.rooms != null) rows += '<div>Комнат: ' + d.rooms + '</div>';
    if (d.floor != null) rows += '<div>Этаж: ' + d.floor + (d.floorsTotal ? '/' + d.floorsTotal : '') + '</div>';
    rows += '<div class="muted">' + escapeHtml(d.address || '') + (d.complex ? ' · ' + escapeHtml(d.complex) : '') + '</div>';
    rows += '<hr><div>Цена: <b>' + fmtRub(d.price) + '</b></div>';
    if (d.totalCostFurnished != null) rows += '<div>Полная стоимость (с мебелью): <b>' + fmtRub(d.totalCostFurnished) + '</b></div>';
    if (d.totalCostUnfurnished != null) rows += '<div>Полная стоимость (без мебели): ' + fmtRub(d.totalCostUnfurnished) + '</div>';
    rows += '<div>Доходность с мебелью: <b>' + (d.yieldFurnished != null ? d.yieldFurnished.toFixed(1) + ' %' : '—') + '</b></div>';
    rows += '<div>Доходность без мебели: ' + (d.yieldUnfurnished != null ? d.yieldUnfurnished.toFixed(1) + ' %' : '—') + '</div>';
    if (d.confidence) rows += '<div class="muted">Уверенность оценки: ' + escapeHtml(d.confidence) + '</div>';
    card.innerHTML = '<button class="close" id="card-close" aria-label="Закрыть">✕</button>' +
      (d.photo ? '<img src="' + escapeAttr(d.photo) + '" alt="">' : '') +
      '<div><b>' + escapeHtml(d.title || 'Объявление') + '</b></div>' + rows +
      '<p><a href="' + escapeAttr(d.url) + '" rel="noopener" target="_blank">Открыть на Авито →</a></p>';
    card.style.display = 'block';
    document.getElementById('card-close').onclick = hideCard;
  }
  function hideCard() { card.style.display = 'none'; }
  document.addEventListener('click', function (e) {
    if (card.style.display === 'block' && !card.contains(e.target) && !e.target.closest('.pin')) hideCard();
  });

  function escapeHtml(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  function escapeAttr(s) { return escapeHtml(s); }

  // Переключатель метрик: подписи точек и агрегаты кластеров пересчитываются,
  // выбор переживает перезагрузку (localStorage).
  var panel = document.getElementById('metric-panel');
  function renderMetricButtons() {
    panel.querySelectorAll('button').forEach(function (b) {
      b.classList.toggle('active', b.dataset.metric === metric);
    });
  }
  panel.addEventListener('click', function (e) {
    var b = e.target.closest('button');
    if (!b) return;
    metric = b.dataset.metric;
    localStorage.setItem('mapMetric', metric);
    renderMetricButtons();
    restyle();
  });
  function restyle() {
    cluster.eachLayer(function (mk) { mk.setIcon(pinIcon(mk.dataset)); });
  }
  renderMetricButtons();

  // Переключатель подложки: выбор в localStorage, применяется на лету.
  var basemapPanel = document.getElementById('basemap-panel');
  function renderBasemapButtons() {
    basemapPanel.querySelectorAll('button').forEach(function (b) {
      b.classList.toggle('active', b.dataset.basemap === basemap);
    });
  }
  basemapPanel.addEventListener('click', function (e) {
    var b = e.target.closest('button');
    if (!b || !BASEMAPS[b.dataset.basemap]) return;
    basemap = b.dataset.basemap;
    localStorage.setItem('mapBasemap', basemap);
    map.removeLayer(tileLayer);
    tileLayer = makeTiles(basemap).addTo(map);
    renderBasemapButtons();
  });
  renderBasemapButtons();

  // Легенда: сворачиваемая, диапазоны и цвета — из MAP_THRESHOLDS.
  var legendHead = document.getElementById('legend-head');
  var legendBody = document.getElementById('legend-body');
  (function buildLegend() {
    var rows = '';
    for (var i = 0; i <= B.length; i++) {
      var lo = i === 0 ? null : B[i - 1];
      var hi = i === B.length ? null : B[i];
      var label = lo == null ? ('< ' + hi + ' %')
        : (hi == null ? ('≥ ' + lo + ' %') : (lo + '–' + hi + ' %'));
      rows += '<div><span class="dot c' + i + '"></span>' + label + '</div>';
    }
    rows += '<div><span class="dot gray"></span>нет данных</div>';
    legendBody.innerHTML = rows;
  })();
  legendHead.onclick = function () {
    var open = legendBody.style.display !== 'none';
    legendBody.style.display = open ? 'none' : 'block';
    legendHead.textContent = 'Доходность, % годовых ' + (open ? '▸' : '▾');
  };

  // Фильтры: состояние в URL + localStorage; применяются на бэкенде
  // (до кластеризации — скрытые объекты в кластеры не попадают).
  var fy = document.getElementById('f-yield');
  var fp = document.getElementById('f-price');
  var fr = document.getElementById('f-rooms');
  var fh = document.getElementById('f-hasroi');
  var qs = new URLSearchParams(location.search);
  fy.value = qs.get('yield_min') || localStorage.getItem('fYield') || '';
  fp.value = qs.get('price_max') || localStorage.getItem('fPrice') || '';
  fr.value = qs.get('rooms') || localStorage.getItem('fRooms') || '';
  fh.checked = qs.get('has_roi') === '1' || localStorage.getItem('fHasROI') === '1';

  function saveFilters() {
    localStorage.setItem('fYield', fy.value);
    localStorage.setItem('fPrice', fp.value);
    localStorage.setItem('fRooms', fr.value);
    localStorage.setItem('fHasROI', fh.checked ? '1' : '0');
  }
  function currentQuery() {
    var b = map.getBounds();
    var p = new URLSearchParams();
    p.set('bbox', [b.getWest(), b.getSouth(), b.getEast(), b.getNorth()].map(function (v) { return v.toFixed(5); }).join(','));
    if (fy.value) p.set('yield_min', fy.value);
    if (fp.value) p.set('price_max', fp.value);
    if (fr.value) p.set('rooms', fr.value);
    if (fh.checked) p.set('has_roi', '1');
    return p;
  }
  document.getElementById('f-apply').onclick = function () { saveFilters(); history.replaceState(null, '', location.pathname + '?' + currentQuery()); load(); };
  document.getElementById('f-reset').onclick = function () {
    fy.value = fp.value = ''; fr.value = ''; fh.checked = false;
    saveFilters(); history.replaceState(null, '', location.pathname); load();
  };

  var inflight = null;
  function load() {
    var p = currentQuery();
    if (inflight) inflight.abort();
    inflight = fetch('/api/map/listings?' + p).then(function (r) { return r.json(); }).then(function (data) {
      inflight = null;
      cluster.clearLayers();
      var marks = data.items.map(marker);
      cluster.addLayers(marks); // bulk-добавление markercluster
    }).catch(function () { inflight = null; });
  }

  // Дебаунс навигации по карте: не дёргаем сеть на каждый кадр.
  var timer = null;
  map.on('moveend zoomend', function () {
    clearTimeout(timer);
    timer = setTimeout(load, 350);
  });
  load();
})();
