package kafka

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
)

// slogDiscard — тихий логгер для тестов.
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeDlq — DLQ-писатель без брокера.
type fakeDlq struct {
	mu     sync.Mutex
	sent   []kafka.Message
	failOn int // с какой попытки Send возвращает ошибку (0 — никогда)
	calls  int
}

func (f *fakeDlq) Send(_ context.Context, m kafka.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failOn > 0 && f.calls >= f.failOn {
		return errors.New("dlq недоступна")
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeDlq) messages() []kafka.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]kafka.Message{}, f.sent...)
}

func testMsg(t *testing.T, eventType string, payload any) kafka.Message {
	t.Helper()
	env, err := NewEnvelope(eventType, payload)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	raw, err := env.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return kafka.Message{
		Topic:     TopicParseRequests,
		Partition: 0,
		Offset:    1,
		Key:       []byte("r-1"),
		Value:     raw,
	}
}

func testConsumer(handler Handler, dlq dlqSink, opts ...ConsumerOption) *Consumer {
	base := []ConsumerOption{
		WithMaxAttempts(3),
		WithBackoff(func(int) time.Duration { return 0 }),
		WithLogger(slogDiscard()),
		withDlqSink(dlq),
	}
	return NewConsumer(Config{Brokers: []string{"localhost:9092"}}, TopicParseRequests, "test", handler, append(base, opts...)...)
}

func newMsg(km kafka.Message) Message {
	env, _ := DecodeEnvelope(km.Value)
	return Message{Topic: km.Topic, Partition: km.Partition, Offset: km.Offset, Key: km.Key, Envelope: *env}
}

// TestProcessRetryThenSuccess — handler падает дважды и выздоравливает:
// DLQ не нужна, сообщение считается обработанным.
func TestProcessRetryThenSuccess(t *testing.T) {
	dlq := &fakeDlq{}
	calls := 0
	c := testConsumer(func(context.Context, Message) error {
		calls++
		if calls < 3 {
			return errors.New("временная ошибка")
		}
		return nil
	}, dlq)

	if err := c.process(context.Background(), testMsg(t, EventTypeParseRequest, ParseRequest{RequestID: "r-1"})); err != nil {
		t.Fatalf("process: %v", err)
	}
	if calls != 3 {
		t.Fatalf("handler вызван %d раз, хочу 3", calls)
	}
	if len(dlq.messages()) != 0 {
		t.Fatalf("DLQ не должен был понадобиться")
	}
}

// TestProcessDLQAfterMaxAttempts — handler падает всегда: после
// maxAttempts сообщение уходит в <topic>-dlq с паспортом в заголовках.
func TestProcessDLQAfterMaxAttempts(t *testing.T) {
	dlq := &fakeDlq{}
	calls := 0
	c := testConsumer(func(context.Context, Message) error {
		calls++
		return errors.New("стабильная ошибка")
	}, dlq)

	km := testMsg(t, EventTypeParseRequest, ParseRequest{RequestID: "r-2"})
	if err := c.process(context.Background(), km); err != nil {
		t.Fatalf("process: %v", err) // после DLQ ошибка наружу не идёт
	}
	if calls != 3 {
		t.Fatalf("handler вызван %d раз, хочу 3 (maxAttempts)", calls)
	}
	sent := dlq.messages()
	if len(sent) != 1 {
		t.Fatalf("в DLQ должно лежать 1 сообщение, лежит %d", len(sent))
	}
	got := sent[0]
	if got.Topic != DLQTopic(TopicParseRequests) {
		t.Fatalf("dlq topic = %q, хочу %q", got.Topic, DLQTopic(TopicParseRequests))
	}
	if string(got.Value) != string(km.Value) {
		t.Fatalf("в DLQ должно лежать исходное сообщение без изменений")
	}
	headers := map[string]string{}
	for _, h := range got.Headers {
		headers[h.Key] = string(h.Value)
	}
	if headers["x-dlq-source-topic"] != TopicParseRequests {
		t.Fatalf("нет x-dlq-source-topic: %v", headers)
	}
	if headers["x-dlq-attempts"] != "3" {
		t.Fatalf("x-dlq-attempts = %q, хочу 3", headers["x-dlq-attempts"])
	}
	if headers["x-dlq-error"] == "" {
		t.Fatalf("нет x-dlq-error: %v", headers)
	}
}

// TestProcessDLQUnavailable — если и DLQ недоступна, ошибка наружу:
// Run не закоммитит offset, сообщение будет доставлено повторно.
func TestProcessDLQUnavailable(t *testing.T) {
	dlq := &fakeDlq{failOn: 1}
	c := testConsumer(func(context.Context, Message) error {
		return errors.New("стабильная ошибка")
	}, dlq)

	err := c.process(context.Background(), testMsg(t, EventTypeParseRequest, ParseRequest{RequestID: "r-3"}))
	if err == nil {
		t.Fatal("хотели ошибку, когда DLQ недоступна")
	}
	if !strings.Contains(err.Error(), "dlq") {
		t.Fatalf("ошибка должна указывать на DLQ: %v", err)
	}
}

// TestProcessMalformedGoesToDLQ — битый конверт идёт в DLQ сразу,
// handler не вызывается (ретраи тут не помогут).
func TestProcessMalformedGoesToDLQ(t *testing.T) {
	dlq := &fakeDlq{}
	handlerCalled := false
	c := testConsumer(func(context.Context, Message) error {
		handlerCalled = true
		return nil
	}, dlq)

	km := testMsg(t, EventTypeParseRequest, ParseRequest{RequestID: "r-4"})
	km.Value = []byte(`{"payload":{}}`) // нет event_id и type
	if err := c.process(context.Background(), km); err != nil {
		t.Fatalf("process: %v", err)
	}
	if handlerCalled {
		t.Fatal("handler не должен был вызываться для битого сообщения")
	}
	if n := len(dlq.messages()); n != 1 {
		t.Fatalf("в DLQ должно быть 1 сообщение, лежит %d", n)
	}
}

// TestProcessDedupeSkipsDuplicate — повторный event_id не доходит до
// handler (идемпотентность по event_id).
func TestProcessDedupeSkipsDuplicate(t *testing.T) {
	dlq := &fakeDlq{}
	calls := 0
	c := testConsumer(func(context.Context, Message) error {
		calls++
		return nil
	}, dlq)

	km := testMsg(t, EventTypeParseRequest, ParseRequest{RequestID: "r-5"})
	for i := 0; i < 2; i++ {
		if err := c.process(context.Background(), km); err != nil {
			t.Fatalf("process #%d: %v", i+1, err)
		}
	}
	if calls != 1 {
		t.Fatalf("handler вызван %d раз, хочу 1 (дубликат пропущен)", calls)
	}
}

// TestProcessShutdownDuringBackoff — отмена ctx во время backoff
// прерывает обработку с ошибкой (сообщение придёт заново после рестарта).
func TestProcessShutdownDuringBackoff(t *testing.T) {
	dlq := &fakeDlq{}
	ctx, cancel := context.WithCancel(context.Background())
	c := testConsumer(func(context.Context, Message) error {
		cancel() // «упал» и дожидается backoff — тут пришёл SIGTERM
		return errors.New("ошибка")
	}, dlq)

	err := c.process(ctx, testMsg(t, EventTypeParseRequest, ParseRequest{RequestID: "r-6"}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("хотели context.Canceled, got %v", err)
	}
	if len(dlq.messages()) != 0 {
		t.Fatal("при остановке сервиса DLQ не должна вызываться")
	}
}
