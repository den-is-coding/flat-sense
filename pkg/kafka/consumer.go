package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/segmentio/kafka-go"
)

// Handler — пользовательская обработка одного сообщения.
// Ошибка означает «не обработано»: консьюмер повторит попытку с
// backoff, а после maxAttempts неудач отправит сообщение в <topic>-dlq.
type Handler func(ctx context.Context, m Message) error

// Message — сообщение группы с уже разобранным конвертом.
type Message struct {
	Topic     string
	Partition int
	Offset    int64
	Key       []byte
	Envelope  Envelope
}

// DecodePayload — раскодировать payload в структуру получателя.
func (m Message) DecodePayload(v any) error {
	if err := json.Unmarshal(m.Envelope.Payload, v); err != nil {
		return fmt.Errorf("kafka: decode payload: %w", err)
	}
	return nil
}

// Consumer — консьюмер-группа: читает топик, коммитит offset только
// после успешной обработки (at-least-once), повторяет обработку с
// backoff, после N неудач уводит сообщение в DLQ, пропускает дубликаты
// по event_id и корректно останавливается по отмене контекста
// (graceful shutdown по SIGTERM — см. пример в doc.go).
type Consumer struct {
	cfg         Config
	topic       string
	groupID     string
	handler     Handler
	log         *slog.Logger
	maxAttempts int
	backoff     func(attempt int) time.Duration
	dedupe      *dedupeSet
	dlq         dlqSink

	readerOnce sync.Once
	reader     *kafka.Reader
}

type consumerOptions struct {
	logger      *slog.Logger
	maxAttempts int
	backoff     func(attempt int) time.Duration
	dedupeTTL   time.Duration
	dlq         dlqSink
}

// ConsumerOption — настройка консьюмера.
type ConsumerOption func(*consumerOptions)

// WithLogger — свой логгер (в сервисах — JSON-логгер приложения).
func WithLogger(l *slog.Logger) ConsumerOption {
	return func(o *consumerOptions) {
		if l != nil {
			o.logger = l
		}
	}
}

// WithMaxAttempts — попыток обработки до отправки в DLQ (по умолчанию 4).
func WithMaxAttempts(n int) ConsumerOption {
	return func(o *consumerOptions) {
		if n > 0 {
			o.maxAttempts = n
		}
	}
}

// WithBackoff — своя функция паузы между попытками (для тестов/тонкой
// настройки). Аргумент — номер завершённой попытки, начиная с 1.
func WithBackoff(f func(attempt int) time.Duration) ConsumerOption {
	return func(o *consumerOptions) {
		if f != nil {
			o.backoff = f
		}
	}
}

// WithDedupeTTL — как долго помнить обработанные event_id (10m по
// умолчанию). Сообщения старше TTL могут быть обработаны повторно.
func WithDedupeTTL(ttl time.Duration) ConsumerOption {
	return func(o *consumerOptions) { o.dedupeTTL = ttl }
}

// withDlqSink — подмена DLQ-писателя (unit-тесты).
func withDlqSink(s dlqSink) ConsumerOption {
	return func(o *consumerOptions) { o.dlq = s }
}

// NewConsumer — консьюмер группы groupID по топику topic.
// groupID у каждого сервиса свой (например, "parser", "analyzer") —
// от него зависит распределение партиций и хранение offset-ов.
func NewConsumer(cfg Config, topic, groupID string, handler Handler, opts ...ConsumerOption) *Consumer {
	o := consumerOptions{
		logger:      DefaultLogger(),
		maxAttempts: defaultMaxAttempts,
		backoff:     defaultBackoff,
		dedupeTTL:   defaultDedupeTTL,
	}
	for _, opt := range opts {
		opt(&o)
	}
	dlq := o.dlq
	if dlq == nil {
		dlq = newRealDlq(cfg)
	}
	return &Consumer{
		cfg:         cfg,
		topic:       topic,
		groupID:     groupID,
		handler:     handler,
		log:         o.logger,
		maxAttempts: o.maxAttempts,
		backoff:     o.backoff,
		dedupe:      newDedupeSet(o.dedupeTTL),
		dlq:         dlq,
	}
}

// Run — основной цикл; блокируется до отмены ctx (SIGTERM) и в этом
// случае возвращает nil. Сообщение, обработка которого началась,
// либо коммитится, либо (при неустранимой ошибке) уходит в DLQ —
// «бросить на полдороге» консьюмер не может.
func (c *Consumer) Run(ctx context.Context) error {
	r := c.readerFor()
	defer r.Close()
	c.log.Info("kafka consumer запущен",
		"topic", c.topic, "group", c.groupID,
		"brokers", strings.Join(c.cfg.Brokers, ","), "max_attempts", c.maxAttempts)

	for {
		km, err := r.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				c.log.Info("kafka consumer остановлен", "topic", c.topic)
				return nil // graceful shutdown
			}
			c.log.Error("kafka: ошибка чтения, повтор через 1с", "err", err, "topic", c.topic)
			if err := sleepBackoff(ctx, time.Second); err != nil {
				return nil
			}
			continue
		}

		if err := c.process(ctx, km); err != nil {
			// Ни обработать, ни уложить в DLQ (например, DLQ недоступна).
			// Не коммитим — сообщение будет доставлено повторно.
			c.log.Error("kafka: сообщение не обработано и не попало в DLQ, будет доставлено повторно",
				"err", err, "topic", km.Topic, "partition", km.Partition, "offset", km.Offset)
			continue
		}
		// WithoutCancel: коммит должен дожить и при остановке сервиса.
		if err := r.CommitMessages(context.WithoutCancel(ctx), km); err != nil {
			c.log.Error("kafka: не удалось закоммитить offset", "err", err, "topic", km.Topic)
		}
	}
}

// process — одна итерация: декодирование, идемпотентность, ретраи, DLQ.
func (c *Consumer) process(ctx context.Context, km kafka.Message) error {
	env, err := DecodeEnvelope(km.Value)
	if err != nil {
		// Битое сообщение повторами не лечится — сразу в DLQ.
		c.log.Error("kafka: некорректный конверт, отправляю в DLQ",
			"err", err, "topic", km.Topic, "partition", km.Partition, "offset", km.Offset)
		return c.toDLQ(ctx, km, 0, err)
	}

	if !c.dedupe.mark(env.EventID) {
		c.log.Info("kafka: дубликат event_id, пропускаю", "event_id", env.EventID, "topic", km.Topic)
		return nil
	}

	msg := Message{
		Topic:     km.Topic,
		Partition: km.Partition,
		Offset:    km.Offset,
		Key:       km.Key,
		Envelope:  *env,
	}
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if lastErr = c.handler(ctx, msg); lastErr == nil {
			return nil
		}
		c.log.Warn("kafka: ошибка обработки",
			"err", lastErr, "attempt", attempt, "max_attempts", c.maxAttempts,
			"event_id", env.EventID, "event_type", env.Type, "topic", km.Topic)
		if attempt < c.maxAttempts {
			if err := sleepBackoff(ctx, c.backoff(attempt)); err != nil {
				return err // остановка сервиса — без коммита, придёт заново
			}
		}
	}
	return c.toDLQ(ctx, km, c.maxAttempts, lastErr)
}

// toDLQ — отправить сообщение в <topic>-dlq. Ошибка отправки в DLQ
// возвращается наружу: тогда offset не коммитится и сообщение придёт заново.
func (c *Consumer) toDLQ(ctx context.Context, km kafka.Message, attempts int, cause error) error {
	if err := c.dlq.Send(ctx, dlqMessage(km, attempts, cause)); err != nil {
		return fmt.Errorf("kafka: send to %s: %w", DLQTopic(km.Topic), err)
	}
	c.log.Warn("kafka: сообщение отправлено в DLQ",
		"topic", km.Topic, "dlq", DLQTopic(km.Topic), "attempts", attempts, "cause", cause)
	return nil
}

// dlqMessage — копия исходного сообщения с заголовками-паспортом.
// Value не трогаем: в DLQ лежит тот же конверт, что и в исходном топике.
func dlqMessage(km kafka.Message, attempts int, cause error) kafka.Message {
	headers := append([]kafka.Header{}, km.Headers...)
	headers = append(headers,
		kafka.Header{Key: "x-dlq-source-topic", Value: []byte(km.Topic)},
		kafka.Header{Key: "x-dlq-attempts", Value: []byte(strconv.Itoa(attempts))},
		kafka.Header{Key: "x-dlq-error", Value: []byte(cause.Error())},
	)
	km.Topic = DLQTopic(km.Topic)
	km.Headers = headers
	return km
}

// readerFor — ленивое создание ридера (чтобы конструктор и unit-тесты
// не требовали живого брокера).
func (c *Consumer) readerFor() *kafka.Reader {
	c.readerOnce.Do(func() {
		c.reader = kafka.NewReader(kafka.ReaderConfig{
			Brokers:  c.cfg.Brokers,
			GroupID:  c.groupID,
			Topic:    c.topic,
			MinBytes: 1,
			MaxBytes: 10 << 20, // 10 MiB
			MaxWait:  500 * time.Millisecond,
		})
	})
	return c.reader
}

// sleepBackoff — пауза с отменой по ctx.
func sleepBackoff(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// dlqSink — куда складываются мёртвые письма (реальная — Kafka, в
// тестах — фейк).
type dlqSink interface {
	Send(ctx context.Context, m kafka.Message) error
}

// realDlq — DLQ-писатель поверх того же модуля kafka-go.
type realDlq struct{ w *kafka.Writer }

func newRealDlq(cfg Config) *realDlq {
	return &realDlq{w: &kafka.Writer{
		Addr:            kafka.TCP(cfg.Brokers...),
		Balancer:        &kafka.Hash{},
		RequiredAcks:    kafka.RequireAll,
		MaxAttempts:     defaultMaxAttempts,
		WriteBackoffMin: defaultBackoffBase,
		WriteBackoffMax: defaultBackoffMax,
	}}
}

func (d *realDlq) Send(ctx context.Context, m kafka.Message) error {
	return d.w.WriteMessages(ctx, m)
}

// dedupeSet — in-memory память обработанных event_id (идемпотентность
// консьюмера). Достаточно для одного экземпляра сервиса; при
// горизонтальном масштабировании дедупликацию нужно вынести во
// внешний стор (Redis) — см. README.
type dedupeSet struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

// порог, после которого чистим протухшие id (чтобы карта не росла вечно)
const dedupeSweepThreshold = 8192

func newDedupeSet(ttl time.Duration) *dedupeSet {
	if ttl <= 0 {
		ttl = defaultDedupeTTL
	}
	return &dedupeSet{seen: make(map[string]time.Time), ttl: ttl}
}

// mark отмечает id обработанным; false — дубликат в пределах TTL.
func (s *dedupeSet) mark(id string) bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.seen[id]; ok && now.Sub(t) < s.ttl {
		return false
	}
	s.seen[id] = now
	if len(s.seen) > dedupeSweepThreshold {
		for k, t := range s.seen {
			if now.Sub(t) >= s.ttl {
				delete(s.seen, k)
			}
		}
	}
	return true
}
