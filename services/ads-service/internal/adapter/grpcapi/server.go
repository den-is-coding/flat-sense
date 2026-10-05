// Package grpcapi — gRPC-адаптер AdsService (контракт issue #2,
// реализация issue #5). Все методы поверх domain.AdRepository.
package grpcapi

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"

	"github.com/yourusername/real-estate-analyzer/ads-service/internal/domain"
)

// Лимиты GetSimilarAds: 0/отрицательный → defaultSimilarLimit,
// больше maxSimilarLimit — обрезается.
const (
	defaultSimilarLimit = 10
	maxSimilarLimit     = 50
)

// Server — реализация adsv1.AdsServiceServer.
type Server struct {
	adsv1.UnimplementedAdsServiceServer
	repo domain.AdRepository
	log  *slog.Logger
}

// NewServer — сервер поверх хранилища проекции.
func NewServer(repo domain.AdRepository, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{repo: repo, log: log}
}

// GetAd — объявление по id. Контракт GetAdRequest содержит только id,
// и в этой схеме id — он же avito_id (первичный ключ проекции ads,
// срез avito_listings.id): отдельного «поиска по строковому avito_id»
// в контракте нет, реализован единственный доступный вариант.
func (s *Server) GetAd(ctx context.Context, req *adsv1.GetAdRequest) (*adsv1.Ad, error) {
	if req.GetId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "ads: id обязателен")
	}
	ad, err := s.repo.GetAd(ctx, req.GetId())
	if err != nil {
		return nil, s.wrap("GetAd", err)
	}
	return ad.ToProto(), nil
}

// CreateAd — создать/обновить объявление (тот же upsert, что и у
// консьюмера parsed-ads: контракт требует метод, MVP-поток идёт через
// Kafka, метод оставлен для прямых клиентов парсера).
func (s *Server) CreateAd(ctx context.Context, req *adsv1.CreateAdRequest) (*adsv1.CreateAdResponse, error) {
	ad, err := domain.AdFromProto(req.GetAd())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	created, err := s.repo.UpsertAd(ctx, ad)
	if err != nil {
		return nil, s.wrap("CreateAd", err)
	}
	s.log.Info("ads: CreateAd", "avito_id", ad.AvitoID, "created", created)
	return &adsv1.CreateAdResponse{Id: ad.AvitoID, Created: created}, nil
}

// UpdateAnalysis — сохранить результат анализа в analysis_results
// (минимальный срез + полный protojson в payload). Объявления нет в
// проекции → codes.NotFound.
func (s *Server) UpdateAnalysis(ctx context.Context, req *adsv1.UpdateAnalysisRequest) (*adsv1.UpdateAnalysisResponse, error) {
	analysis, err := domain.AnalysisFromProto(req.GetAnalysis())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.repo.SaveAnalysis(ctx, analysis); err != nil {
		return nil, s.wrap("UpdateAnalysis", err)
	}
	s.log.Info("ads: UpdateAnalysis", "ad_id", analysis.AdID, "status", analysis.Status)
	return &adsv1.UpdateAnalysisResponse{Ok: true}, nil
}

// GetSimilarAds — похожие объявления (эвристика в postgres.Store:
// тот же ЖК, ближайшие по цене; fallback — город + тип сделки).
func (s *Server) GetSimilarAds(ctx context.Context, req *adsv1.GetSimilarAdsRequest) (*adsv1.GetSimilarAdsResponse, error) {
	if req.GetAdId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "ads: ad_id обязателен")
	}
	src, err := s.repo.GetAd(ctx, req.GetAdId())
	if err != nil {
		return nil, s.wrap("GetSimilarAds", err)
	}
	similar, err := s.repo.SimilarAds(ctx, src, normalizeLimit(req.GetLimit()))
	if err != nil {
		return nil, s.wrap("GetSimilarAds", err)
	}
	resp := &adsv1.GetSimilarAdsResponse{Ads: make([]*adsv1.Ad, 0, len(similar))}
	for _, ad := range similar {
		resp.Ads = append(resp.Ads, ad.ToProto())
	}
	return resp, nil
}

// wrap — ошибка репозитория → gRPC-статус (доступность БД наружу
// как Unavailable, отсутствие — NotFound, остальное — Internal).
func (s *Server) wrap(method string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	default:
		s.log.Error("ads: ошибка репозитория", "method", method, "err", err)
		return status.Error(codes.Unavailable, "ads: хранилище недоступно, попробуйте позже")
	}
}

func normalizeLimit(n int32) int {
	switch {
	case n <= 0:
		return defaultSimilarLimit
	case n > maxSimilarLimit:
		return maxSimilarLimit
	default:
		return int(n)
	}
}
