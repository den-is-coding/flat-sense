//go:build integration

// Интеграционные тесты против живого Kafka (issue #3). Требуют
// брокера на localhost:9092 (deploy/docker-compose.yml: `docker compose
// up -d kafka`); в обычный `go test ./...` не входят — собраны под
// build-тегом integration:
//
//	go test -tags integration ./kafka/
package kafka_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"

	pkgkafka "github.com/yourusername/real-estate-analyzer/pkg/kafka"
)

// quietLogger — глушим логи консьюмера в тестах.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// requireKafka пропускает тест, если брокер недоступен (docker не поднят).
func requireKafka(t *testing.T) pkgkafka.Config {
	t.Helper()
	cfg := pkgkafka.ConfigFromEnv()
	conn, err := kafka.Dial("tcp", cfg.Brokers[0])
	if err != nil {
		t.Skipf("Kafka недоступна на %s: %v (docker compose up -d kafka)", cfg.Brokers[0], err)
	}
	defer conn.Close()
	return cfg
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("it-%s-%d", prefix, time.Now().UnixNano())
}

func mustCreateTopic(t *testing.T, cfg pkgkafka.Config, topic string) {
	t.Helper()
	client := &kafka.Client{Addr: kafka.TCP(cfg.Brokers...), Timeout: 10 * time.Second}
	_, err := client.CreateTopics(context.Background(), &kafka.CreateTopicsRequest{
		Topics: []kafka.TopicConfig{{
			Topic:             topic,
			NumPartitions:     3,
			ReplicationFactor: 1,
		}},
	})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("CreateTopics(%s): %v", topic, err)
	}
	t.Cleanup(func() {
		client.DeleteTopics(context.Background(), &kafka.DeleteTopicsRequest{Topics: []string{topic}})
	})
}

// TestPublishConsumeRoundTrip — продюсер кладёт ParseRequest и
// ParsedAd, консьюмер-группа читает их с полным конвертом.
func TestPublishConsumeRoundTrip(t *testing.T) {
	cfg := requireKafka(t)
	topic := uniqueName("roundtrip")
	mustCreateTopic(t, cfg, topic)

	p := pkgkafka.NewProducer(cfg)
	defer p.Close()
	if err := p.PublishParseRequest(context.Background(), pkgkafka.ParseRequest{
		RequestID: "r-it-1", URL: "https://www.avito.ru/spb/kvartiry/it_1", Source: "avito",
	}); err != nil {
		t.Fatalf("PublishParseRequest: %v", err)
	}
	if err := p.PublishParsedAd(context.Background(), pkgkafka.ParsedAd{RequestID: "r-it-1", Ad: nil}); err != nil {
		t.Fatalf("PublishParsedAd: %v", err)
	}

	got := make(chan pkgkafka.Message, 2)
	handler := func(_ context.Context, m pkgkafka.Message) error {
		got <- m
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := pkgkafka.NewConsumer(cfg, topic, "it-"+topic, handler,
		pkgkafka.WithLogger(quietLogger()))
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run после отмены ctx: %v (хотели graceful nil)", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run не вернулся за 10с после отмены ctx")
		}
	}()

	var sawRequest, sawAd bool
	deadline := time.After(30 * time.Second)
	for n := 0; n < 2; n++ {
		select {
		case m := <-got:
			switch m.Envelope.Type {
			case pkgkafka.EventTypeParseRequest:
				var req pkgkafka.ParseRequest
				if err := m.DecodePayload(&req); err != nil {
					t.Fatalf("DecodePayload(ParseRequest): %v", err)
				}
				if req.RequestID != "r-it-1" || req.Source != "avito" {
					t.Fatalf("ParseRequest разошёлся: %+v", req)
				}
				sawRequest = true
			case pkgkafka.EventTypeParsedAd:
				var ad pkgkafka.ParsedAd
				if err := m.DecodePayload(&ad); err != nil {
					t.Fatalf("DecodePayload(ParsedAd): %v", err)
				}
				if ad.RequestID != "r-it-1" {
					t.Fatalf("ParsedAd разошёлся: %+v", ad)
				}
				sawAd = true
			}
		case <-deadline:
			t.Fatalf("получили %d из 2 сообщений (request=%v ad=%v)", n, sawRequest, sawAd)
		}
	}
	if !sawRequest || !sawAd {
		t.Fatalf("не все типы дошли (request=%v ad=%v)", sawRequest, sawAd)
	}
}

// TestConsumerDLQ — после N неудач сообщение оказывается в
// <topic>-dlq с исходным конвертом и заголовками-паспортом.
func TestConsumerDLQ(t *testing.T) {
	cfg := requireKafka(t)
	topic := uniqueName("dlq")
	mustCreateTopic(t, cfg, topic)
	mustCreateTopic(t, cfg, pkgkafka.DLQTopic(topic))

	p := pkgkafka.NewProducer(cfg)
	defer p.Close()
	if err := p.Publish(context.Background(), topic, []byte("r-it-dlq"), mustEnvelope(t, topic)); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	c := pkgkafka.NewConsumer(cfg, topic, "it-"+topic,
		func(context.Context, pkgkafka.Message) error {
			return fmt.Errorf("обработка всегда ломается")
		},
		pkgkafka.WithMaxAttempts(2),
		pkgkafka.WithBackoff(func(int) time.Duration { return time.Millisecond }),
		pkgkafka.WithLogger(quietLogger()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	// Читаем DLQ обычным ридером (без группы).
	dlqReader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: cfg.Brokers, Topic: pkgkafka.DLQTopic(topic), MinBytes: 1, MaxBytes: 1 << 20,
	})
	defer dlqReader.Close()

	dlqCtx, dlqCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer dlqCancel()
	m, err := dlqReader.ReadMessage(dlqCtx)
	if err != nil {
		t.Fatalf("сообщение не дошло до %s: %v", pkgkafka.DLQTopic(topic), err)
	}
	headers := map[string]string{}
	for _, h := range m.Headers {
		headers[h.Key] = string(h.Value)
	}
	if headers["x-dlq-source-topic"] != topic || headers["x-dlq-attempts"] != "2" {
		t.Fatalf("заголовки DLQ: %v", headers)
	}
	env, err := pkgkafka.DecodeEnvelope(m.Value)
	if err != nil {
		t.Fatalf("в DLQ лежит не конверт: %v", err)
	}
	if env.Type != "it_probe" {
		t.Fatalf("в DLQ чужой конверт: %+v", env)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run не вернулся за 10с")
	}
}

func mustEnvelope(t *testing.T, _ string) *pkgkafka.Envelope {
	t.Helper()
	env, err := pkgkafka.NewEnvelope("it_probe", map[string]string{"probe": "1"})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	return env
}
