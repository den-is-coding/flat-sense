package grpcapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	analyzerv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/analyzer/v1"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/backfill"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeEvaluator / fakeSaver — те же подмены, что в pipeline-тестах.
type fakeEvaluator struct {
	rep   *evaluate.Report
	err   error
	calls []int64
}

func (f *fakeEvaluator) EvaluateByID(_ context.Context, id int64) (*evaluate.Report, error) {
	f.calls = append(f.calls, id)
	return f.rep, f.err
}

type fakeSaver struct {
	rows []backfill.Row
	err  error
}

func (f *fakeSaver) Upsert(_ context.Context, row backfill.Row) error {
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, row)
	return nil
}

func okReport() *evaluate.Report {
	return &evaluate.Report{
		Status:          "ok",
		InputFurnishing: evaluate.Unfurnished,
		Confidence:      "medium",
		Listing:         &evaluate.ListingView{ID: 7907741579, Price: 7_600_000},
		Cluster:         &evaluate.ClusterView{N: 5},
		Scenarios: []evaluate.Scenario{
			{Name: "без мебели", Applicable: true, YieldPct: 4.9, PriceUsed: 7_929_000, RentMedian: 32_000, Comps: 3, DealCosts: 329_000},
			{Name: "с мебелью (после меблировки)", Applicable: true, YieldPct: 4.6, PriceUsed: 8_429_000, RentMedian: 32_000, Comps: 2, FurnishingCost: 500_000, DealCosts: 329_000},
		},
	}
}

// TestEvaluateAd_Ok — gRPC EvaluateAd считает тем же Evaluator и
// возвращает AnalysisResult.
func TestEvaluateAd_Ok(t *testing.T) {
	ev := &fakeEvaluator{rep: okReport()}
	srv := NewServer(ev, &fakeSaver{}, evaluate.DefaultConfig(), quiet())

	res, err := srv.EvaluateAd(context.Background(), &analyzerv1.EvaluateAdRequest{AdId: 7907741579})
	if err != nil {
		t.Fatalf("EvaluateAd: %v", err)
	}
	if len(ev.calls) != 1 || ev.calls[0] != 7907741579 {
		t.Fatalf("evaluate вызовы: %v", ev.calls)
	}
	if res.AdId != 7907741579 || res.Status != "ok" || res.Confidence != "medium" || res.ClusterN != 5 {
		t.Fatalf("AnalysisResult: %v", res)
	}
	if !res.GetFurnished().GetApplicable() || res.GetFurnished().GetFurnishingCost() != 500_000 {
		t.Fatalf("сценарий «с мебелью»: %v", res.Furnished)
	}
}

// TestEvaluateAd_NoRentData — «нет арендных данных» — валидный ответ,
// не ошибка (как в HTTP API).
func TestEvaluateAd_NoRentData(t *testing.T) {
	rep := &evaluate.Report{Status: "no_rent_data", InputFurnishing: evaluate.Unfurnished}
	srv := NewServer(&fakeEvaluator{rep: rep}, &fakeSaver{}, evaluate.DefaultConfig(), quiet())
	res, err := srv.EvaluateAd(context.Background(), &analyzerv1.EvaluateAdRequest{AdId: 42})
	if err != nil {
		t.Fatalf("EvaluateAd: %v", err)
	}
	if res.Status != "no_rent_data" || res.GetUnfurnished().GetApplicable() {
		t.Fatalf("AnalysisResult: %v", res)
	}
}

func TestEvaluateAd_BadRequest(t *testing.T) {
	srv := NewServer(&fakeEvaluator{}, &fakeSaver{}, evaluate.DefaultConfig(), quiet())
	_, err := srv.EvaluateAd(context.Background(), &analyzerv1.EvaluateAdRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestEvaluateAd_InternalError(t *testing.T) {
	srv := NewServer(&fakeEvaluator{err: errors.New("listing 9 not found")}, &fakeSaver{}, evaluate.DefaultConfig(), quiet())
	_, err := srv.EvaluateAd(context.Background(), &analyzerv1.EvaluateAdRequest{AdId: 9})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}

// TestUpdateAnalysis_SavesRow — готовый результат кладётся в кэш тем же
// upsert, что и backfill/поток.
func TestUpdateAnalysis_SavesRow(t *testing.T) {
	sv := &fakeSaver{}
	srv := NewServer(&fakeEvaluator{}, sv, evaluate.DefaultConfig(), quiet())

	resp, err := srv.UpdateAnalysis(context.Background(), &analyzerv1.UpdateAnalysisRequest{
		Analysis: &analyzerv1.AnalysisResult{
			AdId:       7907741579,
			Status:     "ok",
			Furnishing: "unfurnished",
			Confidence: "low",
			ClusterN:   2,
			Unfurnished: &analyzerv1.Scenario{
				Applicable: true, YieldPct: 4.9, RentMedian: 32_000, FullCost: 7_929_000, Comps: 1,
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateAnalysis: %v", err)
	}
	if !resp.Ok {
		t.Fatal("ok = false")
	}
	if len(sv.rows) != 1 {
		t.Fatalf("upsert вызовы: %d", len(sv.rows))
	}
	row := sv.rows[0]
	if row.AdID != 7907741579 || row.Status != "ok" || row.ClusterN == nil || *row.ClusterN != 2 {
		t.Fatalf("строка кэша: %+v", row)
	}
	if row.YieldUnfurnished == nil || *row.YieldUnfurnished != 4.9 {
		t.Fatalf("yieldUnfurnished: %v", row.YieldUnfurnished)
	}
	// furnished неприменим → NULL-колонки.
	if row.YieldFurnished != nil || row.TotalCostFurn != nil {
		t.Fatalf("furnished должен быть NULL: %+v", row)
	}
}

func TestUpdateAnalysis_BadRequest(t *testing.T) {
	srv := NewServer(&fakeEvaluator{}, &fakeSaver{}, evaluate.DefaultConfig(), quiet())
	for name, req := range map[string]*analyzerv1.UpdateAnalysisRequest{
		"nil analysis": {},
		"zero ad_id":   {Analysis: &analyzerv1.AnalysisResult{}},
	} {
		_, err := srv.UpdateAnalysis(context.Background(), req)
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%s: code = %v, want InvalidArgument", name, status.Code(err))
		}
	}
}

func TestUpdateAnalysis_SaverErrorUnavailable(t *testing.T) {
	srv := NewServer(&fakeEvaluator{}, &fakeSaver{err: errors.New("db down")}, evaluate.DefaultConfig(), quiet())
	_, err := srv.UpdateAnalysis(context.Background(), &analyzerv1.UpdateAnalysisRequest{
		Analysis: &analyzerv1.AnalysisResult{AdId: 42, Status: "ok"},
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %v, want Unavailable", status.Code(err))
	}
}

// TestUpdateAnalysis_NilSaver — сервис без БД (-dump-dir): UpdateAnalysis
// отвечает Unavailable, а не паникует.
func TestUpdateAnalysis_NilSaver(t *testing.T) {
	srv := NewServer(&fakeEvaluator{}, nil, evaluate.DefaultConfig(), quiet())
	_, err := srv.UpdateAnalysis(context.Background(), &analyzerv1.UpdateAnalysisRequest{
		Analysis: &analyzerv1.AnalysisResult{AdId: 42, Status: "ok"},
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %v, want Unavailable", status.Code(err))
	}
}
