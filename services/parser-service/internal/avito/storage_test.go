package avito

import (
	"context"
	"os"
	"testing"
	"time"
)

// Интеграционный тест: запускается только при заданном TEST_DSN
// (см. Makefile / README) и живом PostgreSQL с применённой миграцией
// 000010_avito_listings.
func TestStorage_Integration(t *testing.T) {
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		t.Skip("TEST_DSN not set")
	}
	ctx := context.Background()
	s, err := NewStorage(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()

	// идемпотентность: чистим следы прошлых прогонов теста
	if _, err := s.pool.Exec(ctx, `DELETE FROM avito_listings WHERE id=$1`, int64(3221234567)); err != nil {
		t.Fatalf("cleanup listings: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM avito_parse_runs WHERE filters->>'city'='moskva'`); err != nil {
		t.Fatalf("cleanup runs: %v", err)
	}

	l := &Listing{
		ID: 3221234567, URL: "https://www.avito.ru/moskva/kvartiry/test_3221234567",
		URLPath:  "/moskva/kvartiry/test_3221234567",
		Category: "kvartiry", DealType: KindSale,
		Title: "2-к. квартира, 54 м²", Description: "тест",
		Price: 12500000, Currency: "RUB", PricePerM2: 231481, PriceUnit: "м2",
		Rooms: 2, TotalArea: 54, Floor: 5, FloorsTotal: 9, HouseType: "Кирпичный",
		Address: "Москва, улица Ленина, 10", Region: "Москва", City: "Москва",
		Lat: 55.75, Lng: 37.61,
		Geo:        mustJSON(map[string]any{"formattedAddress": "Москва, улица Ленина, 10"}),
		SellerName: "Иван", SellerType: "private",
		Images:     []Image{{URL: "https://img.avito.st/640x480/111.jpg", W: 640, H: 480}},
		ImageCount: 1,
		Views:      1520, Contacts: 34,
		PublishedAt: time.Now().Add(-24 * time.Hour),
		Params:      mustJSON([]Param{{Title: "Этаж", Value: "5 из 9"}}),
		Raw:         mustJSON(map[string]any{"id": 3221234567}),
		SourceTask:  "integration-test",
	}

	isNew, err := s.UpsertListing(ctx, l)
	if err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	if !isNew {
		t.Fatalf("first upsert must be insert")
	}

	// повторный upsert того же объявления с новой ценой — обновление
	l.Price = 11900000
	isNew, err = s.UpsertListing(ctx, l)
	if err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	if isNew {
		t.Fatalf("second upsert must be update")
	}

	runID, err := s.CreateRun(ctx, mustJSON(map[string]any{"city": "moskva"}))
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	err = s.FinishRun(ctx, &ParseRun{
		ID: runID, RequestURL: "https://www.avito.ru/moskva/kvartiry/prodam",
		PagesFetched: 2, ItemsFound: 10, ItemsNew: 8, ItemsUpdated: 2, Status: "ok",
	})
	if err != nil {
		t.Fatalf("finish run: %v", err)
	}
	runs, err := s.ListRuns(ctx, 5)
	if err != nil || len(runs) == 0 {
		t.Fatalf("list runs: %v (%d)", err, len(runs))
	}

	// проверим данные в БД напрямую
	var price int64
	var geo string
	if err := s.pool.QueryRow(ctx,
		`SELECT price, geo::text FROM avito_listings WHERE id=$1`, l.ID).Scan(&price, &geo); err != nil {
		t.Fatalf("query back: %v", err)
	}
	if price != 11900000 {
		t.Fatalf("price in db = %d, want updated 11900000", price)
	}
	if geo == "" || geo == "null" {
		t.Fatalf("geo empty")
	}
	var fin string
	if err := s.pool.QueryRow(ctx,
		`SELECT status FROM avito_parse_runs WHERE id=$1`, runID).Scan(&fin); err != nil {
		t.Fatalf("run query: %v", err)
	}
	if fin != "ok" {
		t.Fatalf("run status = %s", fin)
	}
}
