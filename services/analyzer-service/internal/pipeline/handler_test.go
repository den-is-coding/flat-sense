package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	pkgkafka "github.com/yourusername/real-estate-analyzer/pkg/kafka"
	adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"
	analyzerv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/analyzer/v1"
	"google.golang.org/grpc"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/backfill"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// quiet — глушим логи в тестах.
func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeEvaluator — подменяет evaluate.Evaluator.
type fakeEvaluator struct {
	rep *evaluate.Report
	err error

	calls []int64
}

func (f *fakeEvaluator) EvaluateByID(_ context.Context, id int64) (*evaluate.Report, error) {
	f.calls = append(f.calls, id)
	return f.rep, f.err
}

// fakeSaver — подменяет upsert ad_roi_results.
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

// fakeNotifier — подменяет gRPC-клиент ads-service.
type fakeNotifier struct {
	calls    []*analyzerv1.AnalysisResult
	err      error
	attempts int // суммарно вызовов UpdateAnalysis
}

func (f *fakeNotifier) NotifyAnalysis(_ context.Context, res *analyzerv1.AnalysisResult) error {
	f.attempts++
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, res)
	return nil
}

// okReport — отчёт с двумя применимыми сценариями (как в mapping_test).
func okReport() *evaluate.Report {
	return &evaluate.Report{
		Status:          "ok",
		InputFurnishing: evaluate.Unfurnished,
		Confidence:      "low",
		Listing:         &evaluate.ListingView{ID: 7907741579, Price: 7_600_000},
		Cluster:         &evaluate.ClusterView{N: 2, NFurnished: 1, NUnfurnished: 1},
		Scenarios: []evaluate.Scenario{
			{Name: "без мебели", Applicable: true, YieldPct: 4.9, PriceUsed: 7_929_000, RentMedian: 32_000, Comps: 1, DealCosts: 329_000},
			{Name: "с мебелью (после меблировки)", Applicable: true, YieldPct: 4.6, PriceUsed: 8_429_000, RentMedian: 32_000, Comps: 1, FurnishingCost: 500_000, DealCosts: 329_000},
		},
	}
}

// parsedAdMessage — конверт parsed_ad (как их публикует parser-service).
func parsedAdMessage(t *testing.T, requestID string, ad *adsv1.Ad) pkgkafka.Message {
	t.Helper()
	envl, err := pkgkafka.NewEnvelope(pkgkafka.EventTypeParsedAd, pkgkafka.ParsedAd{RequestID: requestID, Ad: ad})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := envl.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	envl2, err := pkgkafka.DecodeEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	return pkgkafka.Message{Topic: pkgkafka.TopicParsedAds, Envelope: *envl2}
}

func parseErrorMessage(t *testing.T, requestID, cause string) pkgkafka.Message {
	t.Helper()
	envl, err := pkgkafka.NewEnvelope(pkgkafka.EventTypeParseError, pkgkafka.ParseError{RequestID: requestID, Error: cause})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := envl.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	envl2, err := pkgkafka.DecodeEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	return pkgkafka.Message{Topic: pkgkafka.TopicParsedAds, Envelope: *envl2}
}

// TestHandleParsedAd_HappyPath — parsed_ad: evaluate по avito_id →
// строка ad_roi_results → уведомление ads-service, ack (nil).
func TestHandleParsedAd_HappyPath(t *testing.T) {
	ev := &fakeEvaluator{rep: okReport()}
	sv := &fakeSaver{}
	nt := &fakeNotifier{}
	h := NewHandler(ev, sv, nt, evaluate.DefaultConfig(), quiet())

	msg := parsedAdMessage(t, "req-1", &adsv1.Ad{Id: 7907741579, Url: "https://www.avito.ru/x/1_7907741579"})
	if err := h.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if len(ev.calls) != 1 || ev.calls[0] != 7907741579 {
		t.Fatalf("evaluate вызовы: %v", ev.calls)
	}
	if len(sv.rows) != 1 {
		t.Fatalf("upsert вызовы: %d", len(sv.rows))
	}
	row := sv.rows[0]
	if row.AdID != 7907741579 || row.Status != "ok" || row.YieldUnfurnished == nil || row.YieldFurnished == nil {
		t.Fatalf("строка ad_roi_results: %+v", row)
	}
	if len(nt.calls) != 1 {
		t.Fatalf("notify вызовы: %d", len(nt.calls))
	}
	res := nt.calls[0]
	if res.AdId != 7907741579 || res.Status != "ok" || !res.GetUnfurnished().GetApplicable() {
		t.Fatalf("AnalysisResult: %v", res)
	}
	// В downstream уходит ровно то, что в кэше (одна формула).
	if res.GetUnfurnished().GetYieldPct() != *row.YieldUnfurnished {
		t.Fatalf("yield кэша (%v) и downstream (%v) расходятся", *row.YieldUnfurnished, res.GetUnfurnished().GetYieldPct())
	}
	if h.ParseErrorsTotal() != 0 {
		t.Fatalf("parse_errors_total = %d", h.ParseErrorsTotal())
	}
}

// TestHandleParsedAd_NoRentData — «нет арендных данных» — валидный
// исход: строка в кэше (status=no_rent_data), downstream уведомлён, ack.
func TestHandleParsedAd_NoRentData(t *testing.T) {
	rep := &evaluate.Report{
		Status:          "no_rent_data",
		InputFurnishing: evaluate.Unfurnished,
		Notice:          "по дому/ЖК addr:… нет арендных данных",
	}
	h := NewHandler(&fakeEvaluator{rep: rep}, &fakeSaver{}, &fakeNotifier{}, evaluate.DefaultConfig(), quiet())

	msg := parsedAdMessage(t, "req-2", &adsv1.Ad{Id: 42})
	if err := h.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
}

// TestHandleParsedAd_EvaluateError — ошибка evaluate наружу: консьюмер
// повторит (at-least-once), DLQ после maxAttempts.
func TestHandleParsedAd_EvaluateError(t *testing.T) {
	ev := &fakeEvaluator{err: errors.New("listing 42 not found")}
	h := NewHandler(ev, &fakeSaver{}, &fakeNotifier{}, evaluate.DefaultConfig(), quiet())

	msg := parsedAdMessage(t, "req-3", &adsv1.Ad{Id: 42})
	if err := h.HandleMessage(context.Background(), msg); err == nil {
		t.Fatal("ошибка evaluate должна возвращаться наружу (retry консьюмера)")
	}
}

// TestHandleParsedAd_SaverError — ошибка записи ad_roi_results наружу:
// уведомлять downstream без сохранённого кэша нельзя.
func TestHandleParsedAd_SaverError(t *testing.T) {
	nt := &fakeNotifier{}
	h := NewHandler(&fakeEvaluator{rep: okReport()},
		&fakeSaver{err: errors.New("db down")}, nt, evaluate.DefaultConfig(), quiet())

	msg := parsedAdMessage(t, "req-4", &adsv1.Ad{Id: 7907741579})
	if err := h.HandleMessage(context.Background(), msg); err == nil {
		t.Fatal("ошибка upsert должна возвращаться наружу (retry консьюмера)")
	}
	if nt.attempts != 0 {
		t.Fatalf("downstream не должен уведомляться при ошибке кэша: %d", nt.attempts)
	}
}

// TestHandleParsedAd_NotifyFailureIsAcked — ads-service недоступен после
// N ретраев: лог-ошибка, но ack (nil) — результат уже в ad_roi_results,
// поток не валим (критерий #6).
func TestHandleParsedAd_NotifyFailureIsAcked(t *testing.T) {
	nt := &fakeNotifier{err: errors.New("rpc error: Unavailable")}
	h := NewHandler(&fakeEvaluator{rep: okReport()}, &fakeSaver{}, nt, evaluate.DefaultConfig(), quiet())

	msg := parsedAdMessage(t, "req-5", &adsv1.Ad{Id: 7907741579})
	if err := h.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("упавшее уведомление не должно ломать поток: %v", err)
	}
	if nt.attempts != 1 {
		t.Fatalf("fake-нотификатор вызван %d раз", nt.attempts)
	}
}

// TestHandleParsedAd_NonSaleAcked — аренда (rent_long) в окупаемость
// покупки не превращается: ack без расчёта, данные и так в арендном
// пуле avito_listings (тот же критерий отбора, что у backfill #66).
func TestHandleParsedAd_NonSaleAcked(t *testing.T) {
	ev := &fakeEvaluator{rep: okReport()}
	sv := &fakeSaver{}
	nt := &fakeNotifier{}
	h := NewHandler(ev, sv, nt, evaluate.DefaultConfig(), quiet())

	msg := parsedAdMessage(t, "req-9", &adsv1.Ad{Id: 55, DealType: "rent_long"})
	if err := h.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("не-продажа должна ack-аться: %v", err)
	}
	if len(ev.calls) != 0 || len(sv.rows) != 0 || nt.attempts != 0 {
		t.Fatal("не-продажа не должна запускать расчёт")
	}
}

// TestHandleParsedAd_NilAd — parsed_ad без объявления — ошибка наружу
// (битый payload уйдёт в DLQ после ретраев).
func TestHandleParsedAd_NilAd(t *testing.T) {
	h := NewHandler(&fakeEvaluator{}, &fakeSaver{}, &fakeNotifier{}, evaluate.DefaultConfig(), quiet())
	if err := h.HandleMessage(context.Background(), parsedAdMessage(t, "req-6", nil)); err == nil {
		t.Fatal("parsed_ad без Ad должен вернуть ошибку")
	}
	if err := h.HandleMessage(context.Background(), parsedAdMessage(t, "req-6", &adsv1.Ad{})); err == nil {
		t.Fatal("parsed_ad с ad_id=0 должен вернуть ошибку")
	}
}

// TestHandleParseError_Acked — parse_error: лог + счётчик + ack,
// evaluate не вызывается (нечего считать).
func TestHandleParseError_Acked(t *testing.T) {
	ev := &fakeEvaluator{rep: okReport()}
	sv := &fakeSaver{}
	nt := &fakeNotifier{}
	h := NewHandler(ev, sv, nt, evaluate.DefaultConfig(), quiet())

	msg := parseErrorMessage(t, "req-7", "авито заблокировал")
	if err := h.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("parse_error должен ack-аться: %v", err)
	}
	if len(ev.calls) != 0 || len(sv.rows) != 0 || nt.attempts != 0 {
		t.Fatal("parse_error не должен запускать расчёт")
	}
	if h.ParseErrorsTotal() != 1 {
		t.Fatalf("parse_errors_total = %d, want 1", h.ParseErrorsTotal())
	}
}

// TestHandleUnknownEvent — чужой тип события — ошибка (консьюмер уведёт
// в parsed-ads-dlq).
func TestHandleUnknownEvent(t *testing.T) {
	h := NewHandler(&fakeEvaluator{}, &fakeSaver{}, nil, evaluate.DefaultConfig(), quiet())
	envl, err := pkgkafka.NewEnvelope("alien_event", map[string]string{"a": "b"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := envl.Marshal()
	envl2, _ := pkgkafka.DecodeEnvelope(raw)
	if err := h.HandleMessage(context.Background(), pkgkafka.Message{Envelope: *envl2}); err == nil {
		t.Fatal("чужой тип события должен вернуть ошибку")
	}
}

// TestHandleParsedAd_NilNotifier — ADS_GRPC_ADDR пуст → notifier nil:
// расчёт и кэш работают, уведомления пропускаются.
func TestHandleParsedAd_NilNotifier(t *testing.T) {
	sv := &fakeSaver{}
	h := NewHandler(&fakeEvaluator{rep: okReport()}, sv, nil, evaluate.DefaultConfig(), quiet())
	msg := parsedAdMessage(t, "req-8", &adsv1.Ad{Id: 7907741579})
	if err := h.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("nil notifier не должен ломать поток: %v", err)
	}
	if len(sv.rows) != 1 {
		t.Fatalf("кэш не записан: %d", len(sv.rows))
	}
}

// Тесты AdsNotifier: ограниченные ретраи на недоступном ads-service.

// fakeAdsStub — подменяет adsv1.AdsServiceClient (интерфейс proto).
type fakeAdsStub struct {
	failuresLeft int
	calls        int
}

func (f *fakeAdsStub) UpdateAnalysis(_ context.Context, _ *adsv1.UpdateAnalysisRequest, _ ...grpc.CallOption) (*adsv1.UpdateAnalysisResponse, error) {
	f.calls++
	if f.calls <= f.failuresLeft {
		return nil, errors.New("connection refused")
	}
	return &adsv1.UpdateAnalysisResponse{Ok: true}, nil
}

func (f *fakeAdsStub) GetAd(context.Context, *adsv1.GetAdRequest, ...grpc.CallOption) (*adsv1.Ad, error) {
	return nil, errors.New("not implemented in test stub")
}

func (f *fakeAdsStub) CreateAd(context.Context, *adsv1.CreateAdRequest, ...grpc.CallOption) (*adsv1.CreateAdResponse, error) {
	return nil, errors.New("not implemented in test stub")
}

func (f *fakeAdsStub) GetSimilarAds(context.Context, *adsv1.GetSimilarAdsRequest, ...grpc.CallOption) (*adsv1.GetSimilarAdsResponse, error) {
	return nil, errors.New("not implemented in test stub")
}
func TestAdsNotifier_RetryThenSuccess(t *testing.T) {
	stub := &fakeAdsStub{failuresLeft: 2}
	n := &AdsNotifier{
		stub:     stub,
		attempts: 3,
		backoff:  func(int) time.Duration { return 0 },
		log:      quiet(),
	}
	if err := n.NotifyAnalysis(context.Background(), &analyzerv1.AnalysisResult{AdId: 1, Status: "ok"}); err != nil {
		t.Fatalf("ретраи должны привести к успеху: %v", err)
	}
	if stub.calls != 3 {
		t.Fatalf("вызовов UpdateAnalysis = %d, want 3", stub.calls)
	}
}

func TestAdsNotifier_Exhausted(t *testing.T) {
	stub := &fakeAdsStub{failuresLeft: 100}
	n := &AdsNotifier{
		stub:     stub,
		attempts: 3,
		backoff:  func(int) time.Duration { return 0 },
		log:      quiet(),
	}
	err := n.NotifyAnalysis(context.Background(), &analyzerv1.AnalysisResult{AdId: 1})
	if err == nil {
		t.Fatal("после N неудач должна вернуться ошибка (решение — за Handler)")
	}
	if stub.calls != 3 {
		t.Fatalf("вызовов UpdateAnalysis = %d, want 3 (ограниченный ретрай)", stub.calls)
	}
}

func TestAdsNotifier_CancelledContext(t *testing.T) {
	stub := &fakeAdsStub{failuresLeft: 100}
	n := &AdsNotifier{
		stub:     stub,
		attempts: 5,
		backoff:  func(int) time.Duration { return time.Hour },
		log:      quiet(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.NotifyAnalysis(ctx, &analyzerv1.AnalysisResult{AdId: 1}); err == nil {
		t.Fatal("отменённый контекст должен возвращать ошибку (не ждать backoff)")
	}
}
