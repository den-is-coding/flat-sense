package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
)

// Producer — синхронная отправка JSON-сообщений в Kafka.
// Обёртка над segmentio/kafka-go (чистый Go, без cgo — совместимо
// со scratch-образами). WriteMessages блокируется до подтверждения
// брокера (Async=false); при ошибке писатель сам делает до
// maxAttempts повторных попыток с backoff.
type Producer struct {
	writer *kafka.Writer
	log    *slog.Logger
}

type producerOptions struct {
	logger      *slog.Logger
	maxAttempts int
	batchSize   int
}

// ProducerOption — настройка продюсера.
type ProducerOption func(*producerOptions)

// WithLogger — свой логгер (в сервисах — JSON-логгер приложения).
func WithProducerLogger(l *slog.Logger) ProducerOption {
	return func(o *producerOptions) {
		if l != nil {
			o.logger = l
		}
	}
}

// WithProducerRetries — число попыток отправки (пауза между ними —
// экспоненциальный backoff самого kafka-go, WriteBackoffMin/Max).
func WithProducerRetries(maxAttempts int) ProducerOption {
	return func(o *producerOptions) {
		if maxAttempts > 0 {
			o.maxAttempts = maxAttempts
		}
	}
}

// NewProducer — продюсер на весь процесс (писатель потокобезопасен).
//
// Балансировщик kafka.Hash: сообщения с одинаковым ключом (request_id)
// попадают в одну партицию — сохраняется порядок внутри запроса.
func NewProducer(cfg Config, opts ...ProducerOption) *Producer {
	o := producerOptions{
		logger:      DefaultLogger(),
		maxAttempts: defaultMaxAttempts,
		batchSize:   16,
	}
	for _, opt := range opts {
		opt(&o)
	}
	return &Producer{
		writer: &kafka.Writer{
			Addr:            kafka.TCP(cfg.Brokers...),
			Balancer:        &kafka.Hash{},
			RequiredAcks:    kafka.RequireAll,
			BatchSize:       o.batchSize,
			BatchTimeout:    10 * time.Millisecond,
			MaxAttempts:     o.maxAttempts,
			WriteBackoffMin: defaultBackoffBase,
			WriteBackoffMax: defaultBackoffMax,
		},
		log: o.logger,
	}
}

// Publish — отправить готовый конверт. topic и key — на вызывающем:
// для MVP-потока ключом служит request_id (порядок внутри запроса).
func (p *Producer) Publish(ctx context.Context, topic string, key []byte, env *Envelope) error {
	raw, err := env.Marshal()
	if err != nil {
		return err
	}
	msg := kafka.Message{
		Topic:   topic,
		Key:     key,
		Value:   raw,
		Time:    env.OccurredAt,
		Headers: envelopeHeaders(env),
	}
	if err := p.writer.WriteMessages(ctx, msg); err != nil {
		p.log.Error("kafka: не удалось отправить сообщение",
			"err", err, "topic", topic, "event_id", env.EventID, "event_type", env.Type)
		return fmt.Errorf("kafka: publish %s: %w", topic, err)
	}
	return nil
}

// PublishParseRequest — задание парсеру (TopicParseRequests).
func (p *Producer) PublishParseRequest(ctx context.Context, req ParseRequest) error {
	env, err := NewEnvelope(EventTypeParseRequest, req)
	if err != nil {
		return err
	}
	return p.Publish(ctx, TopicParseRequests, []byte(req.RequestID), env)
}

// PublishParsedAd — результат парсинга (TopicParsedAds).
func (p *Producer) PublishParsedAd(ctx context.Context, ad ParsedAd) error {
	env, err := NewEnvelope(EventTypeParsedAd, ad)
	if err != nil {
		return err
	}
	return p.Publish(ctx, TopicParsedAds, []byte(ad.RequestID), env)
}

// Close — дождаться неподтверждённых сообщений и закрыть соединения.
func (p *Producer) Close() error { return p.writer.Close() }

func envelopeHeaders(env *Envelope) []kafka.Header {
	return []kafka.Header{
		{Key: "x-event-id", Value: []byte(env.EventID)},
		{Key: "x-event-type", Value: []byte(env.Type)},
	}
}
