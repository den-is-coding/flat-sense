package grpcapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/yourusername/real-estate-analyzer/ads-service/internal/domain"
	adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"
	analyzerv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/analyzer/v1"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeRepo — фейк domain.AdRepository для тестов gRPC-адаптера.
type fakeRepo struct {
	ads         map[int64]domain.Ad
	upserts     []domain.Ad
	saved       []domain.Analysis
	notFoundFor map[int64]bool // GetAd возвращает NotFound для этих id
}

func newFakeRepo(ads ...domain.Ad) *fakeRepo {
	r := &fakeRepo{ads: map[int64]domain.Ad{}, notFoundFor: map[int64]bool{}}
	for _, ad := range ads {
		r.ads[ad.AvitoID] = ad
	}
	return r
}

func (f *fakeRepo) UpsertAd(_ context.Context, ad domain.Ad) (bool, error) {
	_, exists := f.ads[ad.AvitoID]
	f.upserts = append(f.upserts, ad)
	f.ads[ad.AvitoID] = ad
	return !exists, nil
}

func (f *fakeRepo) GetAd(_ context.Context, avitoID int64) (domain.Ad, error) {
	if f.notFoundFor[avitoID] {
		return domain.Ad{}, domain.ErrNotFound
	}
	ad, ok := f.ads[avitoID]
	if !ok {
		return domain.Ad{}, domain.ErrNotFound
	}
	return ad, nil
}

func (f *fakeRepo) SimilarAds(_ context.Context, ad domain.Ad, limit int) ([]domain.Ad, error) {
	// Фейк проверяет только нормализацию лимита; содержимое — в
	// интеграционных тестах против реального SQL.
	if limit <= 0 || limit > maxSimilarLimit {
		return nil, errors.New("fakeRepo: limit не нормализован")
	}
	out := []domain.Ad{}
	for i := range limit {
		out = append(out, domain.Ad{AvitoID: int64(9000 + i), ResidentialComplex: ad.ResidentialComplex})
	}
	return out, nil
}

func (f *fakeRepo) SaveAnalysis(_ context.Context, a domain.Analysis) error {
	if a.AdID == 666 {
		return domain.ErrNotFound
	}
	f.saved = append(f.saved, a)
	return nil
}

func sampleDomainAd() domain.Ad {
	price := int64(9_900_000)
	rooms := int32(1)
	return domain.Ad{
		AvitoID: 555, URL: "https://www.avito.ru/x", Title: "Студия",
		DealType: "sale", Category: "kvartiry", Price: &price, Rooms: &rooms, Studio: true,
		ResidentialComplex: "Северный парк", City: "Москва",
	}
}

func TestGetAd(t *testing.T) {
	repo := newFakeRepo(sampleDomainAd())
	srv := NewServer(repo, quietLogger())

	got, err := srv.GetAd(context.Background(), &adsv1.GetAdRequest{Id: 555})
	if err != nil {
		t.Fatalf("GetAd: %v", err)
	}
	if got.GetId() != 555 || got.GetTitle() != "Студия" || got.GetPrice() != 9_900_000 ||
		got.GetRooms() != 1 || got.GetResidentialComplex() != "Северный парк" {
		t.Errorf("GetAd вернул не то: %+v", got)
	}

	if _, err := srv.GetAd(context.Background(), &adsv1.GetAdRequest{Id: 404}); status.Code(err) != codes.NotFound {
		t.Errorf("GetAd(404): code = %v, хочу NotFound", status.Code(err))
	}
	if _, err := srv.GetAd(context.Background(), &adsv1.GetAdRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("GetAd(0): code = %v, хочу InvalidArgument", status.Code(err))
	}
}

func TestCreateAd(t *testing.T) {
	repo := newFakeRepo()
	srv := NewServer(repo, quietLogger())

	price := int64(5_000_000)
	resp, err := srv.CreateAd(context.Background(), &adsv1.CreateAdRequest{
		Ad: &adsv1.Ad{Id: 111, Url: "https://www.avito.ru/x", Price: price},
	})
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	if !resp.GetCreated() || resp.GetId() != 111 {
		t.Errorf("CreateAd = (%d, %v), хочу (111, true)", resp.GetId(), resp.GetCreated())
	}
	// Повтор — обновление существующей строки.
	resp, err = srv.CreateAd(context.Background(), &adsv1.CreateAdRequest{
		Ad: &adsv1.Ad{Id: 111, Url: "https://www.avito.ru/x", Price: price},
	})
	if err != nil {
		t.Fatalf("CreateAd повтор: %v", err)
	}
	if resp.GetCreated() {
		t.Error("повторный CreateAd должен сообщать created=false")
	}
	if _, err := srv.CreateAd(context.Background(), &adsv1.CreateAdRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("CreateAd(nil): code = %v, хочу InvalidArgument", status.Code(err))
	}
}

func TestUpdateAnalysis(t *testing.T) {
	repo := newFakeRepo(sampleDomainAd())
	srv := NewServer(repo, quietLogger())

	resp, err := srv.UpdateAnalysis(context.Background(), &adsv1.UpdateAnalysisRequest{
		Analysis: &analyzerv1.AnalysisResult{
			AdId:       555,
			Status:     "ok",
			ComputedAt: nil,
			Furnished:  &analyzerv1.Scenario{Applicable: true, RentMedian: 45000, YieldPct: 4.1, PaybackYears: 24.4},
		},
	})
	if err != nil {
		t.Fatalf("UpdateAnalysis: %v", err)
	}
	if !resp.GetOk() {
		t.Error("UpdateAnalysis должен отвечать ok=true")
	}
	if len(repo.saved) != 1 {
		t.Fatalf("SaveAnalysis вызван %d раз, хочу 1", len(repo.saved))
	}
	a := repo.saved[0]
	if a.AdID != 555 || a.RentForecast == nil || *a.RentForecast != 45000 {
		t.Errorf("сохранённый анализ: %+v", a)
	}

	// Объявления нет в проекции → NotFound (не внутренняя ошибка).
	if _, err := srv.UpdateAnalysis(context.Background(), &adsv1.UpdateAnalysisRequest{
		Analysis: &analyzerv1.AnalysisResult{AdId: 666, Status: "ok"},
	}); status.Code(err) != codes.NotFound {
		t.Errorf("UpdateAnalysis(666): code = %v, хочу NotFound", status.Code(err))
	}
	if _, err := srv.UpdateAnalysis(context.Background(), &adsv1.UpdateAnalysisRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("UpdateAnalysis(nil): code = %v, хочу InvalidArgument", status.Code(err))
	}
}

func TestGetSimilarAds(t *testing.T) {
	repo := newFakeRepo(sampleDomainAd())
	srv := NewServer(repo, quietLogger())
	ctx := context.Background()

	// Лимит по умолчанию.
	resp, err := srv.GetSimilarAds(ctx, &adsv1.GetSimilarAdsRequest{AdId: 555})
	if err != nil {
		t.Fatalf("GetSimilarAds: %v", err)
	}
	if len(resp.GetAds()) != defaultSimilarLimit {
		t.Errorf("без лимита хочу %d похожих, получили %d", defaultSimilarLimit, len(resp.GetAds()))
	}
	// Явный лимит передаётся как есть.
	if _, err := srv.GetSimilarAds(ctx, &adsv1.GetSimilarAdsRequest{AdId: 555, Limit: 5}); err != nil {
		t.Fatalf("GetSimilarAds(limit=5): %v", err)
	}
	// Огромный лимит обрезается.
	if _, err := srv.GetSimilarAds(ctx, &adsv1.GetSimilarAdsRequest{AdId: 555, Limit: 10_000}); err != nil {
		t.Fatalf("GetSimilarAds(limit=10000): %v", err)
	}
	// Исходного объявления нет → NotFound.
	repo.notFoundFor[555] = true
	if _, err := srv.GetSimilarAds(ctx, &adsv1.GetSimilarAdsRequest{AdId: 555}); status.Code(err) != codes.NotFound {
		t.Errorf("GetSimilarAds(нет ad): code = %v, хочу NotFound", status.Code(err))
	}
	if _, err := srv.GetSimilarAds(ctx, &adsv1.GetSimilarAdsRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("GetSimilarAds(0): code = %v, хочу InvalidArgument", status.Code(err))
	}
}
