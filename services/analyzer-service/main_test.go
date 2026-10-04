package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

type fakeSource struct{}

func (fakeSource) ListingByID(_ context.Context, id int64) (*evaluate.Listing, error) {
	return &evaluate.Listing{ID: id, URL: "https://www.avito.ru/x/1_42",
		DealType: "sale", Studio: true, TotalArea: 21.2, Price: 7_958_145,
		Address: "Ул. Тамбасова, корп. 2"}, nil
}

func (fakeSource) RentListings(_ context.Context) ([]evaluate.Listing, error) {
	return nil, nil // арендного пула нет
}

func TestHandleEvaluateBadRequests(t *testing.T) {
	ev := evaluate.NewEvaluator(fakeSource{}, evaluate.DefaultConfig())
	srv := newServer(ev)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /evaluate", srv.handleEvaluate)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/evaluate", "application/json", strings.NewReader("{bad json"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad json: status = %d, want 400", resp.StatusCode)
	}

	resp, err = http.Post(ts.URL+"/evaluate", "application/json", strings.NewReader(`{"foo":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no id: status = %d, want 400", resp.StatusCode)
	}
}

// «Нет арендных данных» — валидный исход: 200 с вежливым отказом, не 500
// (issue #64, критерий 3).
func TestHandleEvaluateNoRentDataPolite(t *testing.T) {
	ev := evaluate.NewEvaluator(fakeSource{}, evaluate.DefaultConfig())
	srv := newServer(ev)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /evaluate", srv.handleEvaluate)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/evaluate", "application/json",
		strings.NewReader(`{"adId": 42}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (вежливый отказ)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "no_rent_data") {
		t.Fatal("в ответе должен быть статус no_rent_data")
	}
}
