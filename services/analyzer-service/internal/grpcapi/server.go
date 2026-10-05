// Package grpcapi — gRPC-адаптер AnalyzerService (контракт issue #2,
// событийный поток issue #6). EvaluateAd считает оценку по id объявления
// (тот же Evaluator, что у HTTP API и консьюмера parsed-ads);
// UpdateAnalysis кладёт готовый результат в кэш ad_roi_results тем же
// upsert, что и backfill #66 — формула в одном месте.
package grpcapi

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	analyzerv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/analyzer/v1"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/analysis"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/backfill"
	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// Evaluator — расчёт оценки (реализует *evaluate.Evaluator).
type Evaluator interface {
	EvaluateByID(ctx context.Context, id int64) (*evaluate.Report, error)
}

// RowSaver — запись строки кэша ad_roi_results (реализует *backfill.Runner).
type RowSaver interface {
	Upsert(ctx context.Context, row backfill.Row) error
}

// Server — реализация analyzerv1.AnalyzerServiceServer.
type Server struct {
	analyzerv1.UnimplementedAnalyzerServiceServer
	ev    Evaluator
	saver RowSaver
	cfg   evaluate.Config
	log   *slog.Logger
}

// NewServer — сервер поверх оценщика и кэша ad_roi_results.
func NewServer(ev Evaluator, saver RowSaver, cfg evaluate.Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{ev: ev, saver: saver, cfg: cfg, log: log}
}

// EvaluateAd — рассчитать оценку объявления-продажи по id
// (avito_listings.id) и вернуть AnalysisResult (в кэш не пишем: запись —
// зона ответственности потока/UpdateAnalysis).
func (s *Server) EvaluateAd(ctx context.Context, req *analyzerv1.EvaluateAdRequest) (*analyzerv1.AnalysisResult, error) {
	if req.GetAdId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "analyzer: ad_id обязателен")
	}
	rep, err := s.ev.EvaluateByID(ctx, req.GetAdId())
	if err != nil {
		s.log.Error("analyzer: EvaluateAd", "ad_id", req.GetAdId(), "err", err)
		return nil, status.Error(codes.Internal, "analyzer: расчёт не удался")
	}
	return analysis.ResultFromReport(rep, s.cfg, req.GetAdId()), nil
}

// UpdateAnalysis — записать готовый результат оценки в ad_roi_results
// (идемпотентный upsert по ad_id).
func (s *Server) UpdateAnalysis(ctx context.Context, req *analyzerv1.UpdateAnalysisRequest) (*analyzerv1.UpdateAnalysisResponse, error) {
	if s.saver == nil {
		// Режим без БД (-dump-dir): кэш недоступен по определению.
		return nil, status.Error(codes.Unavailable, "analyzer: кэш ad_roi_results недоступен (сервис запущен без БД)")
	}
	res := req.GetAnalysis()
	if res == nil || res.GetAdId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "analyzer: analysis.ad_id обязателен")
	}
	row := analysis.RowFromResult(res)
	if err := s.saver.Upsert(ctx, row); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, status.Error(codes.Canceled, err.Error())
		}
		s.log.Error("analyzer: upsert ad_roi_results", "ad_id", row.AdID, "err", err)
		return nil, status.Error(codes.Unavailable, "analyzer: кэш недоступен, попробуйте позже")
	}
	s.log.Info("analyzer: UpdateAnalysis", "ad_id", row.AdID, "status", row.Status)
	return &analyzerv1.UpdateAnalysisResponse{Ok: true}, nil
}
