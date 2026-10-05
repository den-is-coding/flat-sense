package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/yourusername/real-estate-analyzer/parser-service/internal/avito"
	"github.com/yourusername/real-estate-analyzer/pkg/kafka"
	adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"
	"google.golang.org/protobuf/proto"
)

// slogDiscard — тихий логгер для тестов.
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeParser — подмена *avito.Service (порт ItemParser).
type fakeParser struct {
	listing *avito.Listing
	err     error
	urls    []string
}

func (f *fakeParser) ParseOne(_ context.Context, itemURL string) (*avito.Listing, error) {
	f.urls = append(f.urls, itemURL)
	return f.listing, f.err
}

func (f *fakeParser) calls() int { return len(f.urls) }

// fakePublisher — подмена *kafka.Producer (порт EventPublisher).
type fakePublisher struct {
	mu        sync.Mutex
	ads       []kafka.ParsedAd
	errs      []kafka.ParseError
	failAd    error // ошибка публикации parsed_ad
	failParse error // ошибка публикации parse_error
}

func (f *fakePublisher) PublishParsedAd(_ context.Context, ad kafka.ParsedAd) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAd != nil {
		return f.failAd
	}
	f.ads = append(f.ads, ad)
	return nil
}

func (f *fakePublisher) PublishParseError(_ context.Context, e kafka.ParseError) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failParse != nil {
		return f.failParse
	}
	f.errs = append(f.errs, e)
	return nil
}

func (f *fakePublisher) snapshot() (ads []kafka.ParsedAd, errs []kafka.ParseError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]kafka.ParsedAd{}, f.ads...), append([]kafka.ParseError{}, f.errs...)
}

// TestHandleRequest — таблица сценариев обработки ParseRequest (issue #4):
// успех публикует parsed_ad, конечные ошибки (блокировка, 404, пустой
// url, чужой source) — parse_error и ack, остановка сервиса — ошибку
// наружу без публикаций, сбой публикации — ошибку наружу (ретрай).
func TestHandleRequest(t *testing.T) {
	okListing := &avito.Listing{
		ID:          2794567890,
		URL:         "https://www.avito.ru/moskva/kvartiry/2-k_kvartira_2794567890",
		URLPath:     "/moskva/kvartiry/2-k_kvartira_2794567890",
		Title:       "2-к. квартира, 54 м², 5/9 эт.",
		Price:       12_500_000,
		Rooms:       2,
		TotalArea:   54.5,
		Floor:       5,
		FloorsTotal: 9,
		City:        "Москва",
		District:    "Тверской",
		Metro:       "Маяковская",
		DealType:    avito.KindSale,
		Category:    "kvartiry",
		Images: []avito.Image{
			{URL: "https://img.avito.ru/1.jpg", W: 640, H: 480},
			{URL: "https://img.avito.ru/2.jpg", W: 1280, H: 960},
		},
	}

	// stoppedCtx — уже отменённый контекст (имитация SIGTERM).
	stoppedCtx := func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, cancel
	}

	tests := []struct {
		name           string
		req            kafka.ParseRequest
		parser         *fakeParser
		pub            *fakePublisher
		ctx            func() (context.Context, context.CancelFunc)
		wantErr        bool // ошибка наружу (консьюмер повторит/превратит в redelivery)
		wantAds        int  // сколько parsed_ad опубликовано
		wantErrs       int  // сколько parse_error опубликовано
		wantParseCalls int  // сколько раз дёрнули парсер
		check          func(t *testing.T, p *fakePublisher)
	}{
		{
			name: "успех: parsed_ad с Ad из результата парсинга",
			req: kafka.ParseRequest{
				RequestID: "r-1",
				URL:       "https://www.avito.ru/moskva/kvartiry/2-k_kvartira_2794567890",
				Source:    "avito",
			},
			parser:         &fakeParser{listing: okListing},
			pub:            &fakePublisher{},
			wantAds:        1,
			wantParseCalls: 1,
			check: func(t *testing.T, p *fakePublisher) {
				ads, _ := p.snapshot()
				got := ads[0]
				if got.RequestID != "r-1" {
					t.Fatalf("request_id = %q, хочу r-1", got.RequestID)
				}
				want := &adsv1.Ad{
					Id:          okListing.ID,
					Url:         okListing.URL,
					UrlPath:     okListing.URLPath,
					Title:       okListing.Title,
					DealType:    "sale",
					Category:    "kvartiry",
					Price:       okListing.Price,
					Rooms:       proto.Int32(2),
					TotalArea:   proto.Float64(54.5),
					Floor:       proto.Int32(5),
					FloorsTotal: proto.Int32(9),
					City:        "Москва",
					District:    "Тверской",
					Metro:       "Маяковская",
					Photos: []*adsv1.Photo{
						{Url: "https://img.avito.ru/1.jpg", Width: 640, Height: 480},
						{Url: "https://img.avito.ru/2.jpg", Width: 1280, Height: 960},
					},
				}
				if !proto.Equal(want, got.Ad) {
					t.Fatalf("Ad разошёлся:\n хочу %+v\n got %+v", want, got.Ad)
				}
			},
		},
		{
			name:           "ErrBlocked: parse_error и ack (без ретраев парсинга)",
			req:            kafka.ParseRequest{RequestID: "r-2", URL: "https://www.avito.ru/x", Source: "avito"},
			parser:         &fakeParser{err: fmt.Errorf("fetch: %w", avito.ErrBlocked)},
			pub:            &fakePublisher{},
			wantErrs:       1,
			wantParseCalls: 1,
			check: func(t *testing.T, p *fakePublisher) {
				_, errs := p.snapshot()
				if errs[0].RequestID != "r-2" {
					t.Fatalf("request_id = %q, хочу r-2", errs[0].RequestID)
				}
				if !strings.Contains(errs[0].Error, "blocked") {
					t.Fatalf("текст ошибки должен нести причину ErrBlocked: %q", errs[0].Error)
				}
			},
		},
		{
			name:           "ErrNotFound: parse_error и ack",
			req:            kafka.ParseRequest{RequestID: "r-3", URL: "https://www.avito.ru/gone", Source: "avito"},
			parser:         &fakeParser{err: avito.ErrNotFound},
			pub:            &fakePublisher{},
			wantErrs:       1,
			wantParseCalls: 1,
		},
		{
			name:           "пустой url: parse_error без вызова парсера",
			req:            kafka.ParseRequest{RequestID: "r-4", URL: "   ", Source: "avito"},
			parser:         &fakeParser{},
			pub:            &fakePublisher{},
			wantErrs:       1,
			wantParseCalls: 0,
		},
		{
			name:           "чужой source: parse_error без вызова парсера",
			req:            kafka.ParseRequest{RequestID: "r-5", URL: "https://cian.ru/1", Source: "cian"},
			parser:         &fakeParser{},
			pub:            &fakePublisher{},
			wantErrs:       1,
			wantParseCalls: 0,
		},
		{
			name:           "объявление без id: parse_error",
			req:            kafka.ParseRequest{RequestID: "r-6", URL: "https://www.avito.ru/x", Source: "avito"},
			parser:         &fakeParser{listing: &avito.Listing{Title: "без id"}},
			pub:            &fakePublisher{},
			wantErrs:       1,
			wantParseCalls: 1,
		},
		{
			name:           "ошибка публикации parsed_ad: ошибка наружу (ретрай консьюмера)",
			req:            kafka.ParseRequest{RequestID: "r-7", URL: "https://www.avito.ru/x", Source: "avito"},
			parser:         &fakeParser{listing: okListing},
			pub:            &fakePublisher{failAd: errors.New("kafka недоступен")},
			wantErr:        true,
			wantParseCalls: 1,
		},
		{
			name:           "ошибка публикации parse_error: ошибка наружу (ретрай консьюмера)",
			req:            kafka.ParseRequest{RequestID: "r-8", URL: "https://www.avito.ru/x", Source: "avito"},
			parser:         &fakeParser{err: avito.ErrBlocked},
			pub:            &fakePublisher{failParse: errors.New("kafka недоступен")},
			wantErr:        true,
			wantParseCalls: 1,
		},
		{
			name:           "остановка сервиса посреди парсинга: без публикаций, сообщение придёт заново",
			req:            kafka.ParseRequest{RequestID: "r-9", URL: "https://www.avito.ru/x", Source: "avito"},
			parser:         &fakeParser{err: context.Canceled},
			pub:            &fakePublisher{},
			ctx:            stoppedCtx,
			wantErr:        true,
			wantAds:        0,
			wantErrs:       0,
			wantParseCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.ctx != nil {
				c, cancel := tt.ctx()
				defer cancel()
				ctx = c
			}

			h := NewHandler(tt.parser, tt.pub, slogDiscard())
			err := h.HandleRequest(ctx, tt.req)
			if tt.wantErr && err == nil {
				t.Fatal("хотели ошибку наружу, получили nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}

			ads, errs := tt.pub.snapshot()
			if len(ads) != tt.wantAds {
				t.Fatalf("parsed_ad: опубликовано %d, хочу %d", len(ads), tt.wantAds)
			}
			if len(errs) != tt.wantErrs {
				t.Fatalf("parse_error: опубликовано %d, хочу %d", len(errs), tt.wantErrs)
			}
			if tt.parser.calls() != tt.wantParseCalls {
				t.Fatalf("парсер вызван %d раз, хочу %d", tt.parser.calls(), tt.wantParseCalls)
			}
			if tt.check != nil {
				tt.check(t, tt.pub)
			}
		})
	}
}

// TestHandleMessage — декодирование конверта: битый payload и чужой
// тип события возвращают ошибку (консьюмер pkg/kafka уложит их в
// parse-requests-dlq), валидный parse_request доходит до парсера
// и публикует parsed_ad.
func TestHandleMessage(t *testing.T) {
	env, err := kafka.NewEnvelope(kafka.EventTypeParseRequest, kafka.ParseRequest{
		RequestID: "r-11", URL: "https://www.avito.ru/x", Source: "avito",
	})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}

	tests := []struct {
		name    string
		msg     kafka.Message
		wantErr bool
	}{
		{
			name: "валидный parse_request обрабатывается",
			msg:  kafka.Message{Topic: kafka.TopicParseRequests, Envelope: *env},
		},
		{
			name: "битый payload — ошибка (в DLQ через pkg/kafka)",
			msg: kafka.Message{
				Topic: kafka.TopicParseRequests,
				Envelope: kafka.Envelope{
					EventID: "e-1", Type: kafka.EventTypeParseRequest,
					Payload: []byte(`{"request_id":`),
				},
			},
			wantErr: true,
		},
		{
			name: "чужой тип события — ошибка (в DLQ через pkg/kafka)",
			msg: kafka.Message{
				Topic: kafka.TopicParseRequests,
				Envelope: kafka.Envelope{
					EventID: "e-2", Type: kafka.EventTypeParsedAd,
					Payload: []byte(`{}`),
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := &fakeParser{listing: &avito.Listing{ID: 7, URL: "https://www.avito.ru/x"}}
			pub := &fakePublisher{}
			h := NewHandler(parser, pub, slogDiscard())
			err := h.HandleMessage(context.Background(), tt.msg)
			if tt.wantErr && err == nil {
				t.Fatal("хотели ошибку, получили nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}
			if tt.wantErr {
				if n := len(parser.urls); n != 0 {
					t.Fatalf("парсер не должен был вызываться, вызван %d раз", n)
				}
			}
		})
	}

	// Валидное сообщение реально доехало до парсера и публикатора.
	parser := &fakeParser{listing: &avito.Listing{ID: 7, URL: "https://www.avito.ru/x"}}
	pub := &fakePublisher{}
	h := NewHandler(parser, pub, slogDiscard())
	if err := h.HandleMessage(context.Background(), kafka.Message{Topic: kafka.TopicParseRequests, Envelope: *env}); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	ads, errs := pub.snapshot()
	if len(ads) != 1 || len(errs) != 0 {
		t.Fatalf("хотели 1 parsed_ad без ошибок, got ads=%d errs=%d", len(ads), len(errs))
	}
	if ads[0].Ad.Id != 7 {
		t.Fatalf("ad_id = %d, хочу 7", ads[0].Ad.Id)
	}
}

// TestAdFromListing — маппинг Listing → ads.v1.Ad: опциональные поля
// без значений остаются nil (отсутствие = «не распознано», ср. rooms=0
// у студии), фото переносятся целиком.
func TestAdFromListing(t *testing.T) {
	l := &avito.Listing{
		ID:      123,
		URL:     "https://www.avito.ru/x",
		URLPath: "/x",
		Title:   "Студия",
		Studio:  true, // rooms не распознан (студия)
		Price:   5_500_000,
		City:    "СПб",
		Lat:     59.93,
		Images:  []avito.Image{{URL: "https://img/1.jpg", W: 320, H: 240}},
	}
	ad := AdFromListing(l)

	if ad.Id != 123 || ad.Title != "Студия" || !ad.Studio || ad.Price != 5_500_000 || ad.City != "СПб" {
		t.Fatalf("базовые поля разошлись: %+v", ad)
	}
	if ad.Rooms != nil {
		t.Fatalf("rooms у студии должен отсутствовать, got %v", *ad.Rooms)
	}
	if ad.TotalArea != nil || ad.Floor != nil || ad.FloorsTotal != nil || ad.Lng != nil {
		t.Fatal("optional-поля должны быть nil при нулях в Listing")
	}
	if ad.Lat == nil || *ad.Lat != 59.93 {
		t.Fatalf("lat = %v, хочу 59.93", ad.Lat)
	}
	if len(ad.Photos) != 1 || ad.Photos[0].Url != "https://img/1.jpg" ||
		ad.Photos[0].Width != 320 || ad.Photos[0].Height != 240 {
		t.Fatalf("фото разошлись: %+v", ad.Photos)
	}

	// Полное объявление: все optional-поля проставлены.
	full := &avito.Listing{ID: 2, Rooms: 2, TotalArea: 54.5, Floor: 3, FloorsTotal: 9, Lng: 30.3}
	adf := AdFromListing(full)
	if adf.Rooms == nil || *adf.Rooms != 2 ||
		adf.TotalArea == nil || *adf.TotalArea != 54.5 ||
		adf.Floor == nil || *adf.Floor != 3 ||
		adf.FloorsTotal == nil || *adf.FloorsTotal != 9 ||
		adf.Lng == nil || *adf.Lng != 30.3 {
		t.Fatalf("optional-поля полного объявления разошлись: %+v", adf)
	}
}
