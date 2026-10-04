package main

import (
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/source"
)

// Встроенная страница результата (issue #64, раздел «Страница результата»):
// минимальный SSR без зависимости от web/ (#8) — форма на / и отчёт на
// /evaluate-page?ad=<id>. Полноценный фронтенд появится позже.

var pageTmpl = template.Must(template.New("page").
	Funcs(template.FuncMap{
		"price":  func(v int64) string { return formatRub(v) },
		"pricef": func(v float64) string { return formatRub(int64(v + 0.5)) },
	}).
	Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8">
<title>Оценка окупаемости — flat-sense</title>
<style>
 body{font-family:-apple-system,Segoe UI,Roboto,sans-serif;max-width:880px;margin:24px auto;padding:0 16px;color:#1c2733}
 .card{border:1px solid #dfe5ea;border-radius:12px;padding:16px 20px;margin:16px 0}
 .muted{color:#67757f;font-size:14px}
 .warn{background:#fff7e0;border:1px solid #e7c96b;border-radius:8px;padding:10px 14px;margin:10px 0;font-size:14px}
 .scenario{display:flex;gap:24px;flex-wrap:wrap}
 .scenario .num{font-size:26px;font-weight:650}
 table{border-collapse:collapse;font-size:14px}
 td,th{border-bottom:1px solid #e8edf1;padding:6px 10px;text-align:left}
 img{border-radius:8px;max-width:220px}
 form{display:flex;gap:8px}
 input[type=text]{flex:1;padding:9px 12px;border:1px solid #c9d2d9;border-radius:8px;font-size:15px}
 button{padding:9px 18px;border:0;border-radius:8px;background:#0f6fde;color:#fff;font-size:15px;cursor:pointer}
 .refuse{background:#eef4fb;border:1px solid #bcd4ee;border-radius:8px;padding:14px 16px}
</style></head><body>
<h1>Оценка окупаемости студии</h1>
<form action="/evaluate-page" method="get">
  <input type="text" name="ad" placeholder="URL объявления Авито или id (например 3651684187)" value="{{.Query}}">
  <button type="submit">Оценить</button>
</form>
{{if .Report}}{{template "report" .Report}}{{end}}
<p class="muted">Методика: арендные аналоги — студии того же ЖК/дома в диапазоне площади ±20% (правила #55;
ЖК — адреса корпусов арендной кампании или радиус 500 м по координатам). В стоимости учтены:
меблировка {{price .Config.FurnishingCostRUB}} ₽ (если мебели нет), риэлтор {{.Config.RealtorFeePct}}%,
титульное страхование {{.Config.TitleInsurancePct}}%, оформление сделки {{price .Config.DealFixedCostsRUB}} ₽.
Источник данных: локальные дампы парсера.</p>
</body></html>
{{define "report"}}
{{with .Listing}}
<div class="card">
 {{if .Photo}}<img src="{{.Photo}}" alt="">{{end}}
 <h2>{{.Title}}</h2>
 <div><b>{{price .Price}} ₽</b> · {{.Area}} м² · {{.Rooms}}{{if .Floor}} · {{.Floor}} эт.{{end}}</div>
 <div>{{.Address}}</div>
 <div class="muted"><a href="{{.URL}}">оригинал на Авито</a> · ключ дома: {{.HouseKey}}</div>
</div>
{{end}}
{{if .FurnishingNote}}<div class="warn">{{.FurnishingNote}}</div>{{end}}
{{if eq .Status "no_rent_data"}}
 <div class="refuse"><b>Пока не можем посчитать.</b> {{.Notice}}</div>
{{else}}
 <div class="card">
  <h3>Аренда подобных студий в этом ЖК</h3>
  <div class="muted">аналогов: {{.Cluster.N}} (без мебели: {{.Cluster.NUnfurnished}}, с мебелью: {{.Cluster.NFurnished}});
   диапазон площади {{index .Cluster.AreaRange 0}}–{{index .Cluster.AreaRange 1}} м²;
   медиана аренды {{pricef .Cluster.Rent.Median}} ₽/мес (p25–p75: {{pricef .Cluster.Rent.P25}}–{{pricef .Cluster.Rent.P75}})</div>
 </div>
 {{range .Scenarios}}{{if .Applicable}}
 <div class="card">
  <h3>{{.Name}}</h3>
  <div class="scenario">
   <div><div class="muted">предполагаемая сдача</div><div class="num">{{pricef .RentMedian}} ₽/мес</div>
    <div class="muted">p25–p75: {{pricef .RentP25}}–{{pricef .RentP75}}</div></div>
   <div><div class="muted">окупаемость</div><div class="num">{{printf "%.1f" .PaybackYears}} лет</div>
    {{if .FurnishingCost}}<div class="muted">цена +{{price .FurnishingCost}} ₽ на мебель</div>{{end}}
    <div class="muted">+ {{price .DealCosts}} ₽ сделка<br>(риэлтор {{$.Config.RealtorFeePct}}% + титул {{$.Config.TitleInsurancePct}}% + оформление {{price $.Config.DealFixedCostsRUB}} ₽)</div>
    <div class="muted">стоимость в расчёте: {{price .PriceUsed}} ₽</div></div>
   <div><div class="muted">доходность</div><div class="num">{{printf "%.2f" .YieldPct}} %/год</div></div>
   <div><div class="muted">аналогов в сценарии</div><div class="num">{{.Comps}}</div></div>
  </div>
 </div>
 {{end}}{{end}}
 <div class="muted">confidence: {{.Confidence}}{{range .Warnings}} · {{.}}{{end}}</div>
{{end}}
{{end}}
`))

// reportPage — данные для шаблона.
type reportPage struct {
	Query  string
	Report *evaluate.Report
	Config evaluate.Config
}

func (s *apiServer) handleForm(w http.ResponseWriter, r *http.Request) {
	writeHTML(w, http.StatusOK, &reportPage{Config: s.ev.Config})
}

func (s *apiServer) handleEvaluatePage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("ad"))
	if q == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	id := sourceExtractID(q)
	if id == 0 {
		writeHTML(w, http.StatusOK, &reportPage{Query: q, Config: s.ev.Config})
		return
	}
	rep, err := s.ev.EvaluateByID(r.Context(), id)
	if err != nil {
		http.Error(w, "ошибка оценки: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeHTML(w, http.StatusOK, &reportPage{Query: q, Report: rep, Config: s.ev.Config})
}

func writeHTML(w http.ResponseWriter, status int, data *reportPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := pageTmpl.Execute(w, data); err != nil {
		log.Printf("render: %v", err)
	}
}

func sourceExtractID(q string) int64 {
	// чистое число — id; иначе URL.
	if n, err := strconv.ParseInt(q, 10, 64); err == nil {
		return n
	}
	return source.ExtractListingID(q)
}

// formatRub — разряды через тонкий пробел: 7 958 145.
func formatRub(v int64) string {
	s := strconv.FormatInt(v, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}
