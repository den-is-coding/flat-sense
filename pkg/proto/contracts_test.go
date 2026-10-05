// Фикстура-тест контрактов (issue #2): сгенерированный код линкуется,
// сервисы регистрируются в gRPC-сервере, сообщения ходят по кругу.
// Цель — CI гоняет компиляцию контрактов, чтобы поломка генерации или
// несовместимость версий библиотек ловилась раньше интеграции сервисов.
package proto_test

import (
	"context"
	"testing"
	"time"

	adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"
	analyzerv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/analyzer/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- Заглушки серверов: проверка, что типы реализуют сгенерированные
// интерфейсы (компиляция контрактов). ---

type stubAdsService struct {
	adsv1.UnimplementedAdsServiceServer
}

func (s *stubAdsService) GetAd(context.Context, *adsv1.GetAdRequest) (*adsv1.Ad, error) {
	return &adsv1.Ad{}, nil
}

func (s *stubAdsService) CreateAd(context.Context, *adsv1.CreateAdRequest) (*adsv1.CreateAdResponse, error) {
	return &adsv1.CreateAdResponse{}, nil
}

func (s *stubAdsService) UpdateAnalysis(context.Context, *adsv1.UpdateAnalysisRequest) (*adsv1.UpdateAnalysisResponse, error) {
	return &adsv1.UpdateAnalysisResponse{}, nil
}

func (s *stubAdsService) GetSimilarAds(context.Context, *adsv1.GetSimilarAdsRequest) (*adsv1.GetSimilarAdsResponse, error) {
	return &adsv1.GetSimilarAdsResponse{}, nil
}

type stubAnalyzerService struct {
	analyzerv1.UnimplementedAnalyzerServiceServer
}

func (s *stubAnalyzerService) EvaluateAd(context.Context, *analyzerv1.EvaluateAdRequest) (*analyzerv1.AnalysisResult, error) {
	return &analyzerv1.AnalysisResult{}, nil
}

func (s *stubAnalyzerService) UpdateAnalysis(context.Context, *analyzerv1.UpdateAnalysisRequest) (*analyzerv1.UpdateAnalysisResponse, error) {
	return &analyzerv1.UpdateAnalysisResponse{}, nil
}

// TestRegisterServices — оба контракта регистрируются в gRPC-сервере
// со всеми RPC (линковка ServiceDesc).
func TestRegisterServices(t *testing.T) {
	s := grpc.NewServer()
	defer s.Stop()

	adsv1.RegisterAdsServiceServer(s, &stubAdsService{})
	analyzerv1.RegisterAnalyzerServiceServer(s, &stubAnalyzerService{})

	want := map[string][]string{
		"ads.v1.AdsService":           {"GetAd", "CreateAd", "UpdateAnalysis", "GetSimilarAds"},
		"analyzer.v1.AnalyzerService": {"EvaluateAd", "UpdateAnalysis"},
	}
	info := s.GetServiceInfo()
	got := make(map[string][]string, len(info))
	for name, svc := range info {
		methods := make([]string, 0, len(svc.Methods))
		for _, m := range svc.Methods {
			methods = append(methods, m.Name)
		}
		got[name] = methods
	}
	for name, methods := range want {
		gm, ok := got[name]
		if !ok {
			t.Fatalf("сервис %s не зарегистрирован", name)
		}
		if len(gm) != len(methods) {
			t.Fatalf("сервис %s: RPC %v, хочу %v", name, gm, methods)
		}
		set := make(map[string]bool, len(gm))
		for _, m := range gm {
			set[m] = true
		}
		for _, m := range methods {
			if !set[m] {
				t.Fatalf("сервис %s: нет RPC %s (есть %v)", name, m, gm)
			}
		}
	}
}

// TestAnalysisResultRoundTrip — AnalysisResult (вместе со Scenario и
// Timestamp) переживает сериализацию: protobuf-рантайм линкуется.
func TestAnalysisResultRoundTrip(t *testing.T) {
	src := &analyzerv1.AnalysisResult{
		AdId:       123,
		Status:     "ok",
		Furnishing: "unfurnished",
		Unfurnished: &analyzerv1.Scenario{
			Applicable:   true,
			RentMedian:   35000,
			RentP25:      30000,
			RentP75:      40000,
			Comps:        5,
			YieldPct:     7.2,
			PaybackYears: 13.9,
			FullCost:     5_825_000,
		},
		Furnished:      &analyzerv1.Scenario{Applicable: false},
		RealtorFee:     150_000,
		DealCostsOther: 75_000,
		DealCostsTotal: 225_000,
		ClusterN:       5,
		Confidence:     "medium",
		Notice:         "меблировка не определена — считаем «без мебели»",
		ComputedAt:     timestamppb.New(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)),
	}

	blob, err := proto.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var dst analyzerv1.AnalysisResult
	if err := proto.Unmarshal(blob, &dst); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !proto.Equal(src, &dst) {
		t.Fatalf("roundtrip разошёлся:\n src = %+v\n dst = %+v", src, &dst)
	}
}

// TestAdRoundTrip — сообщение Ad покрывает срез avito_listings.
func TestAdRoundTrip(t *testing.T) {
	src := &adsv1.Ad{
		Id:                 2794567890,
		Url:                "https://www.avito.ru/spb/kvartiry/studiya_123",
		UrlPath:            "/spb/kvartiry/studiya_123",
		Title:              "Студия 26 м2",
		DealType:           "sale",
		Category:           "kvartiry",
		Price:              5_500_000,
		Rooms:              proto.Int32(0),
		Studio:             true,
		TotalArea:          proto.Float64(26.4),
		Floor:              proto.Int32(7),
		FloorsTotal:        proto.Int32(17),
		Address:            "СПб, Лиговский пр., 50",
		City:               "Санкт-Петербург",
		District:           "Центральный",
		Metro:              "Площадь Восстания",
		ResidentialComplex: "Лигов City",
		Lat:                proto.Float64(59.917),
		Lng:                proto.Float64(30.344),
		Photos:             []*adsv1.Photo{{Url: "https://img/1.jpg", Width: 640, Height: 480}},
		Description:        "Студия с мебелью и техникой",
	}

	blob, err := proto.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var dst adsv1.Ad
	if err := proto.Unmarshal(blob, &dst); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !proto.Equal(src, &dst) {
		t.Fatalf("roundtrip разошёлся")
	}
	// Присутствие optional-полей (nullable-колонки БД) сохраняется:
	// rooms=0 у студии — это явный ноль, не «поле не задано».
	if dst.Rooms == nil || *dst.Rooms != 0 {
		t.Fatalf("rooms: хочу явный ноль с присутствием, got %v", dst.Rooms)
	}
}
