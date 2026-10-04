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
  // Тема (issue #88, доработка по фидбеку): отдельной кнопки темы нет —
  // тему переключает выбор подложки («Светлая»/«Тёмная» меняют и тайлы,
  // и интерфейс; стандартная ОСМ тему не трогает). Хранение — localStorage
  // 'mapTheme' = light|dark; пока не выбрано — системная prefers-color-scheme.
  // Маркеры/UI перекрашиваются CSS-переменными сами.
  var darkQuery = window.matchMedia('(prefers-color-scheme: dark)');

  function storedTheme() { return localStorage.getItem('mapTheme'); }

  function effectiveDark() {
    var t = storedTheme();
    if (t === 'light' || t === 'dark') return t === 'dark';
    return darkQuery.matches;
  }

  function applyTheme() {
    var t = storedTheme();
    if (t === 'light' || t === 'dark') document.documentElement.setAttribute('data-theme', t);
    else document.documentElement.removeAttribute('data-theme');
  }

  var basemap = localStorage.getItem('mapBasemap');
  if (!BASEMAPS[basemap]) {
    basemap = effectiveDark() ? 'dark' : (storedTheme() === 'light' ? 'light' : 'osm');
  }
  var tileLayer;
  var basemapPanel = document.getElementById('basemap-panel');
  applyTheme();
  tileLayer = makeTiles(basemap).addTo(map);
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
      // кластер красится по своему агрегату выбранной метрики
      // (цена/стоимость — минимум, доходность — максимум)
      var size = Math.min(92, 46 + n * 1.6);
      var html = '<div class="cluster-pin ' + colorClass(agg, metric) + '" style="width:' + size + 'px;height:' + size + 'px">' +
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
  // Диапазоны по метрикам (issue #108): цвет следует выбранной метрике.
  // Индекс нормирован: 0 — худший (красный), последний — лучший (зелёный).
  var T = (window.MAP_THRESHOLDS && window.MAP_THRESHOLDS.thresholds) || {};
  var BOUNDS = {
    yield: T.yield || [5, 8],          // % годовых, «выше = зеленее»
    price: T.price || [7.5e6, 9e6],    // ₽, «дешевле = зеленее»
    cost: T.cost || [9.5e6, 11.5e6]    // ₽
  };

  function bucketIndex(v, m) {
    if (v == null) return -1;
    var b = BOUNDS[m] || [];
    var i = 0;
    if (m === 'yield') {
      // «выше = зеленее»: индекс = число перешагнутых границ
      while (i < b.length && v >= b[i]) i++;
    } else {
      // «дешевле = зеленее»: индекс = число границ, которые значение
      // НЕ перешагнуло снизу (дешёвое значение — последний индекс)
      var above = 0;
      for (var j = 0; j < b.length; j++) {
        if (v > b[j]) above++;
      }
      i = b.length - above;
    }
    return i; // 0 — худший (красный) … b.length — лучший (зелёный)
  }

  function colorClass(v, m) {
    var i = bucketIndex(v, m);
    return i < 0 ? 'gray' : ('b' + i);
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
    var m = METRICS[metric];
    var v = m.value(d);
    var text = m.fmt(v);
    // обёртка с translate(-50%,-50%) центрирует метку любой ширины на точке
    return L.divIcon({
      className: '',
      html: '<div class="pin-wrap"><div class="pin ' + colorClass(v, metric) + '">' + text + '</div></div>',
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
    var html = '<button class="close" id="card-close" aria-label="Закрыть"><i data-lucide="x"></i></button>';
    if (d.photo) html += '<img class="photo" src="' + escapeAttr(d.photo) + '" alt="">';

    // шапка: цена объявления + ЖК, адрес — вторично
    html += '<div class="price-row"><span class="price">' + fmtRub(d.price) + '</span>' +
      (d.complex ? '<span class="jk-badge">' + escapeHtml(d.complex) + '</span>' : '') + '</div>';
    var meta = [];
    if (d.address) meta.push(escapeHtml(d.address));
    if (d.studio) meta.push('студия');
    else if (d.rooms != null) meta.push(d.rooms + '-к');
    if (d.area != null) meta.push(d.area + ' м²');
    if (d.floor != null) meta.push('эт. ' + d.floor + (d.floorsTotal ? '/' + d.floorsTotal : ''));
    html += '<div class="addr">' + meta.join(' · ') + '</div><hr>';

    // ключевой блок: ожидаемая цена сдачи + вилка p25–p75 (оценка #64).
    // Ключевой сценарий — по меблировке объявления; вторая строка —
    // альтернативный сценарий. Нет данных — прочерк с объяснением.
    var furn = { name: 'С мебелью', med: d.rentMedianFurnished, p25: d.rentP25Furnished, p75: d.rentP75Furnished,
      comps: d.compsFurnished, y: d.yieldFurnished, cls: 's-furn' };
    var unf = { name: 'Без мебели', med: d.rentMedianUnfurnished, p25: d.rentP25Unfurnished, p75: d.rentP75Unfurnished,
      comps: d.compsUnfurnished, y: d.yieldUnfurnished, cls: 's-unfurn' };
    var key = d.inputFurnishing === 'furnished' ? furn : unf;
    if (key.med == null && furn.med != null) key = furn;   // своего сценария нет — показываем доступный
    else if (key.med == null && unf.med != null) key = unf;
    if (furn.med == null && unf.med == null) {
      var why = d.dealType === 'rent_long'
        ? 'оценка окупаемости считается для объявлений о продаже'
        : 'по этому ЖК нет арендных данных — расчёт не выполнен';
      html += '<div class="key-data"><div class="lbl">Ожидаемая цена сдачи</div>' +
        '<div class="main">—</div><div class="range-n">' + escapeHtml(why) + '</div></div>';
    } else {
      html += '<div class="key-data"><div class="lbl">Ожидаемая цена сдачи (' +
        (key.name === 'Без мебели' ? 'без мебели' : 'с мебелью') + ')</div>' +
        '<div class="main">' + fmtRub(key.med) + '/мес</div>' +
        '<div class="range-row"><span class="range">' + fmtRub(key.p25) + ' – ' + fmtRub(key.p75) + '</span>' +
        '<span class="range-n"> · p25–p75' + (key.comps ? ' · ' + key.comps + ' аналогов ЖК' : '') + '</span></div></div>';
      // сценарии: обе строки (доступные)
      var scen = '';
      [furn, unf].forEach(function (sc) {
        if (sc.med == null) return;
        var years = sc.y != null ? (100 / sc.y).toFixed(1) : '—';
        scen += '<div class="row ' + sc.cls + '"><span class="sdot"></span>' + sc.name + ': ' +
          fmtRub(sc.med) + ' · ' + (sc.y != null ? sc.y.toFixed(1) + '%' : '—') + ' · ' + years + ' лет</div>';
      });
      if (scen) html += '<div class="scenarios">' + scen + '</div>';
      html += '<div class="muted">Полная стоимость: ' +
        (d.totalCostFurnished != null ? 'с мебелью ' + fmtRub(d.totalCostFurnished) : '') +
        (d.totalCostFurnished != null && d.totalCostUnfurnished != null ? ' · ' : '') +
        (d.totalCostUnfurnished != null ? 'без мебели ' + fmtRub(d.totalCostUnfurnished) : '') +
        (d.confidence ? ' · уверенность ' + escapeHtml(d.confidence) : '') + '</div>';
    }

    html += '<a class="avito" href="' + escapeAttr(d.url) + '" rel="noopener" target="_blank">Открыть на Авито →</a>';
    card.innerHTML = html;
    card.style.display = 'block';
    document.getElementById('card-close').onclick = hideCard;
    if (window.lucide) window.lucide.createIcons();
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
    buildLegend(); // легенда = те же пороги, что красят точки
    renderMarks(); // пересобрать пины и кластеры с новой метрикой
  });
  renderMetricButtons();

  // Переключатель подложки: выбор в localStorage, применяется на лету.
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
    // «Светлая»/«Тёмная» меняют и интерфейс; ОСМ — только тайлы
    if (basemap === 'light' || basemap === 'dark') {
      localStorage.setItem('mapTheme', basemap);
      applyTheme();
    }
    renderBasemapButtons();
  });
  renderBasemapButtons();

  if (window.lucide) window.lucide.createIcons();

  // Бургер-навигация (#108): список разделов в одном месте, легко
  // расширять; цели ЖК/Блог/Тарифы — фреймы #102/#103/#104 (заглушки).
  var NAV = [
    { href: '/', label: 'Главная', icon: 'home' },
    { href: '/map', label: 'Карта', icon: 'map' },
    { href: '#', label: 'ЖК-аналитика', icon: 'building-2' },
    { href: '#', label: 'Блог', icon: 'newspaper' },
    { href: '#', label: 'Тарифы', icon: 'credit-card' },
    { href: '#', label: 'Мои объекты', icon: 'folder-open' }
  ];
  var drawer = document.getElementById('drawer');
  var drawerOverlay = document.getElementById('drawer-overlay');
  (function buildDrawer() {
    var here = location.pathname;
    var nav = document.getElementById('drawer-nav');
    var html = '';
    NAV.forEach(function (item) {
      var active = item.href !== '#' && here === item.href;
      html += '<a href="' + item.href + '"' + (active ? ' class="active" aria-current="page"' : '') + '>' +
        '<i data-lucide="' + item.icon + '"></i>' + item.label + '</a>';
    });
    nav.innerHTML = html;
  })();
  function toggleDrawer(open) {
    drawer.classList.toggle('open', open);
    drawerOverlay.classList.toggle('open', open);
  }
  document.getElementById('burger').addEventListener('click', function () {
    toggleDrawer(!drawer.classList.contains('open'));
    if (window.lucide) window.lucide.createIcons();
  });
  drawerOverlay.addEventListener('click', function () { toggleDrawer(false); });
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape') toggleDrawer(false);
  });

  // Легенда (#108): следует выбранной метрике — те же пороги, что красят
  // точки; строка-примечание о правиле агрегации кластера.
  var legendHead = document.getElementById('legend-head');
  var legendBody = document.getElementById('legend-body');
  var LEGEND_TITLES = { price: 'Цена, ₽', cost: 'Полная стоимость, ₽', yield: 'Доходность, % год' };
  function legendLabel(m, lo, hi) {
    var f = m.fmtShort;
    if (lo == null) return '≤ ' + f(hi);
    if (hi == null) return '> ' + f(lo);
    return f(lo) + '–' + f(hi);
  }
  function buildLegend() {
    var m = METRICS[metric];
    var b = BOUNDS[metric] || [];
    var n = b.length; // диапазонов n+1; b0 — граница «хуже/лучше»
    var rows = '';
    // лучший диапазон — первый в легенде: для цены «≤ b0», для доходности «≥ bLast»
    for (var i = n; i >= 0; i--) {
      var cls = 'b' + i;
      var label;
      if (metric === 'yield') {
        var lo = i === 0 ? null : b[i - 1];
        var hi = i === n ? null : b[i];
        label = legendLabelYield(lo, hi);
      } else {
        // цена/стоимость: i=n → «≤ b0» (лучший), i=0 → «> bLast» (худший)
        if (i === n) label = '≤ ' + m.fmtShort(b[0]);
        else if (i === 0) label = '> ' + m.fmtShort(b[n - 1]);
        else label = m.fmtShort(b[n - i - 1]) + '–' + m.fmtShort(b[n - i]);
      }
      rows += '<div><span class="ldot ' + cls + '"></span>' + label + '</div>';
    }
    rows += '<div><span class="ldot gray"></span>нет данных</div>';
    rows += '<div class="note">в кластере — ' + (m.agg === 'max' ? 'максимум' : 'минимум') + '</div>';
    legendBody.innerHTML = rows;
    legendHead.textContent = LEGEND_TITLES[metric] + ' ▾';
  }
  function legendLabelYield(lo, hi) {
    if (lo == null) return '< ' + hi + ' %';
    if (hi == null) return '≥ ' + lo + ' %';
    return lo + '–' + hi + ' %';
  }
  legendHead.onclick = function () {
    var open = legendBody.style.display !== 'none';
    legendBody.style.display = open ? 'none' : 'block';
    legendHead.textContent = LEGEND_TITLES[metric] + (open ? ' ▸' : ' ▾');
  };
  buildLegend();

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
  // Фильтры применяются автоматически (issue #88, фидбек): числа — с
  // дебаунсом при вводе, селект/чекбокс — сразу; «Применить» не нужна.
  var filterTimer = null;
  function applyFilters() {
    saveFilters();
    history.replaceState(null, '', location.pathname + '?' + currentQuery());
    load();
  }
  fy.addEventListener('input', function () {
    clearTimeout(filterTimer);
    filterTimer = setTimeout(applyFilters, 400);
  });
  fp.addEventListener('input', function () {
    clearTimeout(filterTimer);
    filterTimer = setTimeout(applyFilters, 400);
  });
  fr.addEventListener('change', applyFilters);
  fh.addEventListener('change', applyFilters);
  document.getElementById('f-reset').onclick = function () {
    fy.value = fp.value = ''; fr.value = ''; fh.checked = false;
    applyFilters();
  };

  var inflight = null;
  var marks = []; // кэш маркеров текущей выдачи
  function renderMarks() {
    cluster.clearLayers();
    cluster.addLayers(marks); // bulk-добавление markercluster
  }
  function load() {
    var p = currentQuery();
    if (inflight) inflight.abort();
    inflight = fetch('/api/map/listings?' + p).then(function (r) { return r.json(); }).then(function (data) {
      inflight = null;
      marks = data.items.map(marker);
      renderMarks();
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
