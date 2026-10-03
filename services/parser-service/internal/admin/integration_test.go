package admin

import (
	"context"
	"os"
	"testing"

	"github.com/yourusername/real-estate-analyzer/parser-service/internal/avito"
)

// Интеграционный тест: запускается только при заданном TEST_DSN
// (живой PostgreSQL с применёнными миграциями 000010 и 000011).
func TestStore_Integration(t *testing.T) {
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		t.Skip("TEST_DSN not set")
	}
	ctx := context.Background()
	av, err := avito.NewStorage(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer av.Close()
	store := NewStore(av.Pool())

	const testID = int64(3221234599)
	if _, err := av.Pool().Exec(ctx, `DELETE FROM avito_listings WHERE id=$1`, testID); err != nil {
		t.Fatal(err)
	}
	defer av.Pool().Exec(ctx, `DELETE FROM avito_listings WHERE id=$1`, testID)

	l := &avito.Listing{
		ID: testID, URL: "https://www.avito.ru/spb/kvartiry/admin_test", URLPath: "/spb/kvartiry/admin_test",
		Category: "kvartiry", DealType: avito.KindSale,
		Title: "Студия, 26 м²", Price: 7_500_000, TotalArea: 26, Studio: true,
		Floor: 5, FloorsTotal: 17, Address: "СПб, Лиговский пр., 50",
		City: "Санкт-Петербург", ResidentialComplex: "ЖК Тестовый",
		SourceTask: "admin-integration-test",
	}
	if _, err := av.UpsertListing(ctx, l); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// фильтр по ЖК + пагинация
	page, err := store.List(ctx, ListingFilters{Complex: "ЖК Тестовый", Limit: 10, Page: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("list: total=%d items=%d, want 1/1", page.Total, len(page.Items))
	}
	got := page.Items[0]
	if got.ResidentialComplex != "ЖК Тестовый" || got.Price == nil || *got.Price != 7_500_000 {
		t.Fatalf("row mismatch: %+v", got)
	}
	// цена за м² вычислена (7_500_000 / 26)
	if got.PricePerM2 == nil || *got.PricePerM2 != 288_461 {
		t.Fatalf("price per m2 = %v", got.PricePerM2)
	}
	if got.HasCoords {
		t.Fatal("seeded listing must have no coords")
	}

	// фильтр «без координат» находит запись
	nf := false
	page2, err := store.List(ctx, ListingFilters{Complex: "ЖК Тестовый", HasCoords: &nf})
	if err != nil {
		t.Fatal(err)
	}
	if page2.Total != 1 {
		t.Fatalf("hasCoords=false: total=%d, want 1", page2.Total)
	}

	// Get по id
	row, err := store.Get(ctx, testID)
	if err != nil || row.ID != testID {
		t.Fatalf("get: %v %+v", err, row)
	}
	// Get по несуществующему id → (nil, nil), обработчик отдаёт 404
	if row, err := store.Get(ctx, 1); err != nil || row != nil {
		t.Fatalf("get missing id: (%v, %v), want (nil, nil)", row, err)
	}
}
