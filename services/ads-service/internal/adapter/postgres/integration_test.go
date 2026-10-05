//go:build integration

// Интеграционные тесты ads-service против живого PostgreSQL (issue #5).
// По образцу pkg/kafka: в обычный `go test ./...` не входят, собраны
// под build-тегом integration и требуют БД с накаченными миграциями:
//
//	docker compose -f deploy/docker-compose.yml up -d postgres migrate
//	DATABASE_URL='postgres://analyzer:secret@localhost:5432/analyzer?sslmode=disable' \
//	  go test -tags integration ./internal/adapter/postgres/
package postgres_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourusername/real-estate-analyzer/ads-service/internal/adapter/postgres"
	"github.com/yourusername/real-estate-analyzer/ads-service/internal/domain"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// requireDB — живая БД с миграциями; иначе тест пропускается.
// Закрытие пула регистрируется первым cleanup'ом — оно выполнится
// последним, после очистки данных тестов.
func requireDB(t *testing.T) *postgres.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://analyzer:secret@localhost:5432/analyzer?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("неверный DSN: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("PostgreSQL недоступен на %s: %v", dsn, err)
	}
	var (
		adsTbl, resultsTbl any
	)
	if err := pool.QueryRow(ctx, "SELECT to_regclass('ads'), to_regclass('analysis_results')").Scan(&adsTbl, &resultsTbl); err != nil ||
		adsTbl == nil || resultsTbl == nil {
		pool.Close()
		t.Skip("миграции 000018 не применены (make migrate)")
	}
	t.Cleanup(pool.Close)
	return postgres.NewStore(pool)
}

// uniqueID — уникальный avito_id, чтобы тесты не спорили с данными
// парсера в одной БД.
func uniqueID() int64 { return time.Now().UnixNano()%1_000_000_000 + 9_000_000_000 }

func TestUpsertGetRoundTrip(t *testing.T) {
	store := requireDB(t)

	ctx := context.Background()

	id := uniqueID()
	t.Cleanup(func() { cleanup(t, store, id) })

	rooms, floor := int32(2), int32(7)
	area := 61.5
	price := int64(14_300_000)
	ad := domain.Ad{
		AvitoID: id, URL: "https://www.avito.ru/moskva/kvartiry/it_" + itoa(id),
		URLPath: "/moskva/kvartiry/it_" + itoa(id),
		Title:   "2-к квартира, 61,5 м²", DealType: "sale", Category: "kvartiry",
		Price: &price, PriceCurrency: "RUB", Rooms: &rooms, Studio: false,
		TotalArea: &area, Floor: &floor, FloorsTotal: &floor,
		Address: "Москва, ТТК 24/3", City: "Москва", District: "Тверской",
		Metro: "Белорусская", ResidentialComplex: "IT-ЖК " + itoa(id),
		Lat: nil, Lng: nil,
		Photos:      []domain.Photo{{URL: "https://img/it1.jpg", Width: 1280, Height: 960}},
		Description: "описание", RequestID: "r-it-1",
	}

	created, err := store.UpsertAd(ctx, ad)
	if err != nil {
		t.Fatalf("UpsertAd: %v", err)
	}
	if !created {
		t.Error("первый upsert должен создавать строку (created=true)")
	}

	got, err := store.GetAd(ctx, id)
	if err != nil {
		t.Fatalf("GetAd: %v", err)
	}
	if got.AvitoID != ad.AvitoID || got.Title != ad.Title || got.ResidentialComplex != ad.ResidentialComplex {
		t.Errorf("GetAd потерял поля: %+v", got)
	}
	if got.Price == nil || *got.Price != price || got.Rooms == nil || *got.Rooms != 2 ||
		got.TotalArea == nil || *got.TotalArea != 61.5 || got.Floor == nil || *got.Floor != 7 {
		t.Errorf("GetAd потерял опциональные поля: %+v", got)
	}
	if got.Lat != nil || got.Lng != nil {
		t.Errorf("Lat/Lng должны быть nil: %v/%v", got.Lat, got.Lng)
	}
	if len(got.Photos) != 1 || got.Photos[0].URL != "https://img/it1.jpg" || got.Photos[0].Width != 1280 {
		t.Errorf("photos jsonb разошёлся: %+v", got.Photos)
	}
	if got.RequestID != "r-it-1" {
		t.Errorf("RequestID = %q", got.RequestID)
	}

	// Повтор (at-least-once) — обновление той же строки.
	ad.Title = "обновлённый заголовок"
	ad.RequestID = "r-it-2"
	created, err = store.UpsertAd(ctx, ad)
	if err != nil {
		t.Fatalf("UpsertAd повтор: %v", err)
	}
	if created {
		t.Error("повторный upsert должен обновлять (created=false)")
	}
	got, err = store.GetAd(ctx, id)
	if err != nil {
		t.Fatalf("GetAd после повтора: %v", err)
	}
	if got.Title != "обновлённый заголовок" || got.RequestID != "r-it-2" {
		t.Errorf("повторный upsert не обновил строку: %+v", got)
	}
	if !got.CreatedAt.Equal(got.UpdatedAt) && got.UpdatedAt.Before(got.CreatedAt) {
		t.Errorf("updated_at (%v) раньше created_at (%v)", got.UpdatedAt, got.CreatedAt)
	}
}

func TestGetAdNotFound(t *testing.T) {
	store := requireDB(t)

	if _, err := store.GetAd(context.Background(), uniqueID()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetAd(нет строки): %v, хочу ErrNotFound", err)
	}
}

// Эвристика похожих: тот же ЖК, ближайшие по цене, limit.
func TestSimilarAdsByComplex(t *testing.T) {
	store := requireDB(t)

	ctx := context.Background()

	complexName := "IT-ЖК similar " + itoa(uniqueID())
	src := mustUpsert(t, store, complexName, "", "sale", 5_100_000)
	t.Cleanup(func() { cleanup(t, store, src) })

	near := mustUpsert(t, store, complexName, "", "sale", 5_050_000)  // разница 50k
	far := mustUpsert(t, store, complexName, "", "sale", 6_400_000)   // разница 1.3M
	mustUpsert(t, store, complexName, "", "sale", 0)                  // без цены — не кандидат
	other := mustUpsert(t, store, "другой ЖК", "", "sale", 5_101_000) // другой ЖК — не кандидат
	t.Cleanup(func() { cleanup(t, store, near, far, other) })

	similar, err := store.SimilarAds(ctx, mustGet(t, store, src), 2)
	if err != nil {
		t.Fatalf("SimilarAds: %v", err)
	}
	if len(similar) != 2 {
		t.Fatalf("хочу 2 похожих, получили %d", len(similar))
	}
	if similar[0].AvitoID != near || similar[1].AvitoID != far {
		t.Errorf("порядок по близости цены нарушен: [%d, %d], хочу [%d, %d]",
			similar[0].AvitoID, similar[1].AvitoID, near, far)
	}
	for _, s := range similar {
		if s.AvitoID == src {
			t.Error("исходное объявление не должно попадать в похожие")
		}
	}
}

// Fallback: нет ЖК — тот же город + тип сделки.
func TestSimilarAdsFallbackCity(t *testing.T) {
	store := requireDB(t)

	ctx := context.Background()

	city := "Город-" + itoa(uniqueID())
	src := mustUpsert(t, store, "", city, "rent_long", 70_000)
	t.Cleanup(func() { cleanup(t, store, src) })
	same := mustUpsert(t, store, "", city, "rent_long", 69_000)
	otherDeal := mustUpsert(t, store, "", city, "sale", 69_500)
	t.Cleanup(func() { cleanup(t, store, same, otherDeal) })

	similar, err := store.SimilarAds(ctx, mustGet(t, store, src), 10)
	if err != nil {
		t.Fatalf("SimilarAds: %v", err)
	}
	if len(similar) != 1 || similar[0].AvitoID != same {
		t.Errorf("fallback по городу: [%v], хочу [%d]", similarIDs(similar), same)
	}
}

// Ни ЖК, ни города — пустой список, а не ошибка.
func TestSimilarAdsNoHeuristic(t *testing.T) {
	store := requireDB(t)

	ctx := context.Background()

	src := mustUpsert(t, store, "", "", "sale", 1_000_000)
	t.Cleanup(func() { cleanup(t, store, src) })
	similar, err := store.SimilarAds(ctx, mustGet(t, store, src), 10)
	if err != nil {
		t.Fatalf("SimilarAds: %v", err)
	}
	if len(similar) != 0 {
		t.Errorf("хочу пустой список, получили %d", len(similar))
	}
}

func TestSaveAnalysisUpsert(t *testing.T) {
	store := requireDB(t)

	ctx := context.Background()

	id := mustUpsert(t, store, "IT-ЖК analysis "+itoa(uniqueID()), "", "sale", 8_000_000)
	t.Cleanup(func() { cleanup(t, store, id) })

	rent, yield, payback := 45_000.0, 4.1, 24.4
	a := domain.Analysis{
		AdID: id, Status: "ok",
		RentForecast: &rent, YieldPercent: &yield, PaybackYears: &payback,
		Payload: []byte(`{"adId":` + itoa(id) + `,"status":"ok"}`),
	}
	if err := store.SaveAnalysis(ctx, a); err != nil {
		t.Fatalf("SaveAnalysis: %v", err)
	}
	// Повтор — перезапись (upsert).
	rent2 := 46_000.0
	a.RentForecast = &rent2
	a.Status = "no_rent_data"
	if err := store.SaveAnalysis(ctx, a); err != nil {
		t.Fatalf("SaveAnalysis повтор: %v", err)
	}

	var (
		status  string
		rf      *float64
		payload []byte
	)
	if err := store.Pool().QueryRow(ctx,
		"SELECT status, rent_forecast, payload FROM analysis_results WHERE ad_id = $1", id,
	).Scan(&status, &rf, &payload); err != nil {
		t.Fatalf("select analysis_results: %v", err)
	}
	if status != "no_rent_data" || rf == nil || *rf != rent2 {
		t.Errorf("перезапись анализа не удалась: status=%s rent_forecast=%v", status, rf)
	}
	if len(payload) == 0 {
		t.Error("payload пуст")
	}

	// Объявления нет в проекции → ErrNotFound (FK), а не внутренняя ошибка.
	if err := store.SaveAnalysis(ctx, domain.Analysis{AdID: uniqueID(), Status: "ok", Payload: []byte("{}")}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SaveAnalysis(нет ad): %v, хочу ErrNotFound", err)
	}
}

// ---------- helpers ----------

func mustUpsert(t *testing.T, store *postgres.Store, complexName, city, dealType string, price int64) int64 {
	t.Helper()
	id := uniqueID()
	ad := domain.Ad{
		AvitoID: id, URL: "https://www.avito.ru/x/" + itoa(id), Title: "тест " + itoa(id),
		DealType: dealType, Category: "kvartiry", ResidentialComplex: complexName, City: city,
		RequestID: "r-test",
	}
	if price != 0 {
		p := price
		ad.Price = &p
	}
	if _, err := store.UpsertAd(context.Background(), ad); err != nil {
		t.Fatalf("UpsertAd: %v", err)
	}
	return id
}

func mustGet(t *testing.T, store *postgres.Store, id int64) domain.Ad {
	t.Helper()
	ad, err := store.GetAd(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAd: %v", err)
	}
	return ad
}

func cleanup(t *testing.T, store *postgres.Store, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		if _, err := store.Pool().Exec(context.Background(),
			"DELETE FROM analysis_results WHERE ad_id = $1", id); err != nil {
			t.Errorf("cleanup analysis %d: %v", id, err)
		}
		if _, err := store.Pool().Exec(context.Background(),
			"DELETE FROM ads WHERE avito_id = $1", id); err != nil {
			t.Errorf("cleanup ad %d: %v", id, err)
		}
	}
}

func similarIDs(ads []domain.Ad) []int64 {
	out := make([]int64, 0, len(ads))
	for _, a := range ads {
		out = append(out, a.AvitoID)
	}
	return out
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
