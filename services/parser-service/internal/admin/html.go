package admin

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

type loginData struct {
	Error string
}

var funcs = template.FuncMap{
	"commaint": func(v int64) string {
		// целое с разделителями разрядов (цены в рублях)
		s := strconv.FormatInt(v, 10)
		n := len(s)
		if v < 0 {
			n--
		}
		if n <= 3 {
			return s
		}
		var out []byte
		for i, c := range []byte(s) {
			if i > 0 && (len(s)-i)%3 == 0 {
				out = append(out, ' ')
			}
			out = append(out, c)
		}
		return string(out)
	},
	"floorStr": func(f, ft *int) string {
		if f == nil {
			return ""
		}
		if ft == nil || *ft == 0 {
			return strconv.Itoa(*f)
		}
		return strconv.Itoa(*f) + "/" + strconv.Itoa(*ft)
	},
	"roomsStr": func(r *int, studio bool) string {
		if studio {
			return "студия"
		}
		if r != nil && *r > 0 {
			return strconv.Itoa(*r) + "-к"
		}
		return ""
	},
	"areaStr": func(a *float64) string {
		if a == nil {
			return ""
		}
		return strconv.FormatFloat(*a, 'f', -1, 64)
	},
	"shortdate": func(s *string) string {
		if s == nil || len(*s) < 10 {
			return ""
		}
		return (*s)[:10]
	},
	"mark": func(f ListingFilters, col string) string {
		if f.SortBy == col {
			if f.SortDir == "asc" {
				return " ▲"
			}
			return " ▼"
		}
		return ""
	},
	"hasNext": func(total, limit, page int) bool { return total > limit*page },
	"sub1":    func(n int) int { return n - 1 },
	"add1":    func(n int) int { return n + 1 },
	"rownum":  func(start, i int) int { return start + i },
	"qse": func(f ListingFilters, key, val string) string {
		q := url.Values{}
		q.Set("q", f.Search)
		q.Set("city", f.City)
		q.Set("complex", f.Complex)
		if f.Rooms >= 0 {
			q.Set("rooms", strconv.Itoa(f.Rooms))
		}
		if f.PriceMin > 0 {
			q.Set("priceMin", strconv.FormatInt(f.PriceMin, 10))
		}
		if f.PriceMax > 0 {
			q.Set("priceMax", strconv.FormatInt(f.PriceMax, 10))
		}
		q.Set("sort", f.SortBy)
		q.Set("dir", f.SortDir)
		q.Set(key, val)
		return "/admin?" + q.Encode()
	},
}

var loginTmpl = template.Must(template.New("login").Funcs(funcs).Parse(`<!doctype html>
<html lang="ru">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>Вход — flat-sense admin</title>
<style>body{font:14px/1.45 system-ui,sans-serif;background:#f5f6f8}
.login{max-width:320px;margin:12vh auto;background:#fff;border:1px solid #ddd;padding:24px}
.login input{width:100%;box-sizing:border-box;margin:6px 0 12px;padding:6px}
.err{color:#b3261e}</style></head>
<body><div class="login"><h2>Вход в админку</h2>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/admin/login">
<input name="login" placeholder="Логин" autocomplete="username" required>
<input name="password" type="password" placeholder="Пароль" autocomplete="current-password" required>
<button type="submit">Войти</button>
</form></div></body></html>
`))

var tableTmpl = template.Must(template.New("table").Funcs(funcs).Parse(`<!doctype html>
<html lang="ru">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>Объявления — flat-sense admin</title>
<style>
 body{font:14px/1.45 system-ui,sans-serif;margin:0;color:#1a1a1a;background:#f5f6f8}
 header{background:#232a35;color:#fff;padding:10px 20px;display:flex;gap:16px;align-items:center}
 header span{color:#9aa4b2}
 header .total{color:#fff;font-size:15px}
 header .total b{font-size:19px;color:#ffd166}
 .wrap{padding:16px 20px}
 table{border-collapse:collapse;width:100%;background:#fff;font-variant-numeric:tabular-nums}
 th,td{border:1px solid #ddd;padding:6px 8px;text-align:left;white-space:nowrap}
 th{background:#eceff3}
 th a{color:inherit;text-decoration:none}
 td.num{text-align:right}
 .ok{color:#0a7d32}.miss{color:#b3261e}
 form.filters{display:flex;flex-wrap:wrap;gap:8px;margin-bottom:12px;background:#fff;padding:10px;border:1px solid #ddd}
 form.filters input,form.filters select{padding:4px 6px}
 .pager{margin-top:12px;display:flex;gap:12px}
 .muted{color:#666}
</style></head>
<body>
<header><b>flat-sense admin</b>
<span class="total">Всего объявлений: <b>{{.Page.Total}}</b>{{if .RowsTo}} · показано {{.RowsFrom}}–{{.RowsTo}}{{end}}</span>
<form method="post" action="/admin/logout" style="margin-left:auto"><button>Выйти</button></form></header>
<div class="wrap">
<form class="filters" method="get" action="/admin">
<input name="q" value="{{.Filters.Search}}" placeholder="Поиск: адрес / ЖК / заголовок">
<input name="city" value="{{.Filters.City}}" placeholder="Город">
<input name="complex" value="{{.Filters.Complex}}" placeholder="ЖК">
<select name="rooms"><option value="">комнаты</option>
<option value="0" {{if eq .StudioFilter "1"}}selected{{end}}>студия</option>
<option value="1">1</option><option value="2">2</option><option value="3">3</option></select>
<input name="priceMin" value="{{if .Filters.PriceMin}}{{.Filters.PriceMin}}{{end}}" placeholder="цена от" size="10">
<input name="priceMax" value="{{if .Filters.PriceMax}}{{.Filters.PriceMax}}{{end}}" placeholder="цена до" size="10">
<select name="hasCoords"><option value="">координаты</option>
<option value="1" {{if eq .CoordsFilter "1"}}selected{{end}}>есть</option>
<option value="0" {{if eq .CoordsFilter "0"}}selected{{end}}>нет</option></select>
<input type="hidden" name="sort" value="{{.Filters.SortBy}}">
<input type="hidden" name="dir" value="{{.Filters.SortDir}}">
<button>Применить</button></form>
<div style="overflow-x:auto">
<table>
<thead><tr>
<th>#</th><th>ID</th><th>Заголовок</th>
<th><a href="{{qse .Filters "sort" "price"}}">Цена{{mark .Filters "price"}}</a></th>
<th><a href="{{qse .Filters "sort" "total_area"}}">м²{{mark .Filters "total_area"}}</a></th>
<th>Этаж</th><th>Комнаты</th><th>Цена/м²</th>
<th>Адрес</th><th>ЖК</th><th>Город</th><th>Район</th>
<th>Тип дома</th><th>Год</th><th>Ремонт</th><th>Коорд.</th>
<th>Фото</th><th>Опубл.</th>
<th><a href="{{qse .Filters "sort" "first_seen_at"}}">Перв. парсинг{{mark .Filters "first_seen_at"}}</a></th>
<th><a href="{{qse .Filters "sort" "last_seen_at"}}">Обновл.{{mark .Filters "last_seen_at"}}</a></th>
</tr></thead>
<tbody>
{{range $i, $row := .Page.Items}}<tr>
<td class="num">{{rownum $.RowsFrom $i}}</td>
<td class="num"><a href="{{$row.URL}}" rel="noopener" target="_blank">{{$row.ID}}</a></td>
<td>{{$row.Title}}</td>
<td class="num">{{if $row.Price}}{{commaint $row.Price}}{{end}}</td>
<td class="num">{{areaStr $row.TotalArea}}</td>
<td class="num">{{floorStr $row.Floor $row.FloorsTotal}}</td>
<td>{{roomsStr $row.Rooms $row.Studio}}</td>
<td class="num">{{if $row.PricePerM2}}{{commaint $row.PricePerM2}}{{end}}</td>
<td>{{$row.Address}}</td><td>{{$row.ResidentialComplex}}</td>
<td>{{$row.City}}</td><td>{{$row.District}}</td>
<td>{{$row.HouseType}}</td><td class="num">{{if $row.YearBuilt}}{{$row.YearBuilt}}{{end}}</td>
<td>{{$row.Renovation}}</td>
<td>{{if $row.HasCoords}}<span class="ok">✔</span>{{else}}<span class="miss">✘</span>{{end}}</td>
<td class="num">{{if $row.ImageCount}}{{$row.ImageCount}}{{end}}</td>
<td>{{shortdate $row.PublishedAt}}</td>
<td>{{shortdate $row.FirstSeenAt}}</td><td>{{shortdate $row.LastSeenAt}}</td>
</tr>{{end}}
</tbody></table></div>
<div class="pager">
{{if gt .Page.Page 1}}<a href="{{qse .Filters "page" (print (sub1 .Page.Page))}}">← Назад</a>{{end}}
<span>стр. {{.Page.Page}}</span>
{{if hasNext .Page.Total .Page.Limit .Page.Page}}<a href="{{qse .Filters "page" (print (add1 .Page.Page))}}">Вперёд →</a>{{end}}
</div></div></body></html>
`))

func renderLogin(w http.ResponseWriter, d loginData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = loginTmpl.Execute(w, d)
}

func renderTable(w http.ResponseWriter, f ListingFilters, page *ListingPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	coords := ""
	if f.HasCoords != nil {
		if *f.HasCoords {
			coords = "1"
		} else {
			coords = "0"
		}
	}
	studioFilter := ""
	if f.Studio != nil && *f.Studio {
		studioFilter = "1"
	}
	rowsFrom, rowsTo := 0, 0
	if n := len(page.Items); n > 0 {
		rowsFrom = (page.Page-1)*page.Limit + 1
		rowsTo = rowsFrom + n - 1
	}
	data := struct {
		Filters      ListingFilters
		Page         *ListingPage
		CoordsFilter string
		StudioFilter string
		RowsFrom     int
		RowsTo       int
	}{f, page, coords, studioFilter, rowsFrom, rowsTo}
	_ = tableTmpl.Execute(w, data)
}
