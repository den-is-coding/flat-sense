package consumer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	pkgkafka "github.com/yourusername/real-estate-analyzer/pkg/kafka"
	adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"

	"github.com/yourusername/real-estate-analyzer/ads-service/internal/domain"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSaver — фейк domain.AdRepository-части, нужной консьюмеру.
type fakeSaver struct {
	got      []domain.Ad
	failWith error
}

func (f *fakeSaver) UpsertAd(_ context.Context, ad domain.Ad) (bool, error) {
	if f.failWith != nil {
		return false, f.failWith
	}
	f.got = append(f.got, ad)
	return true, nil
}

// msg — собрать kafka.Message с конвертом (как это делает консьюмер).
func msg(t *testing.T, eventType string, payload any) pkgkafka.Message {
	t.Helper()
	env, err := pkgkafka.NewEnvelope(eventType, payload)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	return pkgkafka.Message{Topic: pkgkafka.TopicParsedAds, Envelope: *env}
}

func sampleAd() *adsv1.Ad {
	price := int64(9_900_000)
	return &adsv1.Ad{Id: 555, Url: "https://www.avito.ru/x", Title: "Студия", Price: price, Studio: true}
}

// parsed_ad → upsert в проекцию с request_id из события.
func TestHandleParsedAd(t *testing.T) {
	saver := &fakeSaver{}
	h := NewHandler(saver, quietLogger())

	err := h.HandleMessage(context.Background(), msg(t, pkgkafka.EventTypeParsedAd,
		pkgkafka.ParsedAd{RequestID: "r-42", Ad: sampleAd()}))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if len(saver.got) != 1 {
		t.Fatalf("UpsertAd вызван %d раз, хочу 1", len(saver.got))
	}
	ad := saver.got[0]
	if ad.AvitoID != 555 || ad.Title != "Студия" || ad.RequestID != "r-42" {
		t.Errorf("сохранено неверное объявление: %+v", ad)
	}
	if ad.Price == nil || *ad.Price != 9_900_000 {
		t.Errorf("Price = %v, хочу 9900000", ad.Price)
	}
	if !ad.Studio {
		t.Error("Studio = false, хочу true")
	}
}

// parse_error — конечный результат: лог + счётчик, ack (nil — offset
// коммитится, ретраев нет, хранилище не трогается).
func TestHandleParseErrorAcked(t *testing.T) {
	saver := &fakeSaver{}
	h := NewHandler(saver, quietLogger())

	for i := 1; i <= 2; i++ {
		err := h.HandleMessage(context.Background(), msg(t, pkgkafka.EventTypeParseError,
			pkgkafka.ParseError{RequestID: "r-err", Error: "авито заблокировал"}))
		if err != nil {
			t.Fatalf("HandleMessage(parse_error): %v", err)
		}
		if got := h.ParseErrorsTotal(); got != int64(i) {
			t.Fatalf("ParseErrorsTotal = %d, хочу %d", got, i)
		}
	}
	if len(saver.got) != 0 {
		t.Errorf("parse_error не должен писать в хранилище, записано %d", len(saver.got))
	}
}

// Битый payload у валидного конверта и чужие типы событий → ошибка
// наружу: консьюмер уложит сообщение в parsed-ads-dlq.
func TestHandleBadMessages(t *testing.T) {
	saver := &fakeSaver{}
	h := NewHandler(saver, quietLogger())
	ctx := context.Background()

	cases := []struct {
		name string
		m    pkgkafka.Message
	}{
		{"битый payload parsed_ad", msg(t, pkgkafka.EventTypeParsedAd, map[string]any{"ad": 123})},
		{"битый payload parse_error", msg(t, pkgkafka.EventTypeParseError, map[string]any{"request_id": 123})},
		{"чужой тип события", msg(t, pkgkafka.EventTypeParseRequest, pkgkafka.ParseRequest{})},
		{"объявление nil", msg(t, pkgkafka.EventTypeParsedAd, pkgkafka.ParsedAd{})},
		{"объявление без id", msg(t, pkgkafka.EventTypeParsedAd,
			pkgkafka.ParsedAd{Ad: &adsv1.Ad{Title: "без id"}})},
	}
	for _, tc := range cases {
		if err := h.HandleMessage(ctx, tc.m); err == nil {
			t.Errorf("%s: ждали ошибку, получили nil", tc.name)
		}
	}
	if len(saver.got) != 0 {
		t.Errorf("битые сообщения не должны доходить до хранилища: %+v", saver.got)
	}
}

// Ошибка БД возвращается наружу: at-least-once повторит доставку,
// после maxAttempts сообщение уйдёт в DLQ. Паники быть не должно.
func TestHandleSaverError(t *testing.T) {
	saver := &fakeSaver{failWith: errors.New("db недоступна")}
	h := NewHandler(saver, quietLogger())

	err := h.HandleMessage(context.Background(), msg(t, pkgkafka.EventTypeParsedAd,
		pkgkafka.ParsedAd{RequestID: "r-1", Ad: sampleAd()}))
	if err == nil {
		t.Fatal("ждали ошибку от хранилища, получили nil")
	}
	if !errors.Is(err, saver.failWith) {
		t.Errorf("ошибка потеряла причину: %v", err)
	}
}
