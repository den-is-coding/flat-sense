package kafka

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"
)

// Envelope — JSON-конверт всех Kafka-сообщений flat-sense (issue #3).
// Пайплайн работает в семантике at-least-once: консьюмер обязан
// обеспечивать идемпотентность, для чего и существует event_id —
// консьюмер пропускает уже обработанные события (см. Consumer).
type Envelope struct {
	// EventID — UUIDv4, генерируется продюсером; ключ идемпотентности.
	EventID string `json:"event_id"`
	// Type — тип события (EventTypeParseRequest, EventTypeParsedAd, ...).
	Type string `json:"type"`
	// OccurredAt — момент публикации (UTC).
	OccurredAt time.Time `json:"occurred_at"`
	// Payload — само сообщение (ParseRequest, ParsedAd, ...) в JSON.
	Payload json.RawMessage `json:"payload"`
}

// Типы событий MVP-потока (значения поля type конверта).
const (
	EventTypeParseRequest = "parse_request"
	EventTypeParsedAd     = "parsed_ad"
)

// NewEnvelope — собрать конверт: payload сериализуется в JSON,
// event_id генерируется (UUIDv4), occurred_at — текущее время UTC.
func NewEnvelope(eventType string, payload any) (*Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("kafka: marshal payload: %w", err)
	}
	id, err := newEventID()
	if err != nil {
		return nil, fmt.Errorf("kafka: generate event_id: %w", err)
	}
	return &Envelope{
		EventID:    id,
		Type:       eventType,
		OccurredAt: time.Now().UTC(),
		Payload:    raw,
	}, nil
}

// Marshal — сериализовать конверт (то, что кладётся в kafka.Message.Value).
func (e *Envelope) Marshal() ([]byte, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("kafka: marshal envelope: %w", err)
	}
	return raw, nil
}

// DecodeEnvelope — разобрать конверт из тела сообщения Kafka.
// Обязательны event_id и type — без них сообщение считается битым
// и консьюмер отправляет его в DLQ.
func DecodeEnvelope(data []byte) (*Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("kafka: decode envelope: %w", err)
	}
	if env.EventID == "" {
		return nil, fmt.Errorf("kafka: envelope без event_id")
	}
	if env.Type == "" {
		return nil, fmt.Errorf("kafka: envelope без type")
	}
	return &env, nil
}

// DecodePayload — раскодировать payload в структуру получателя.
func (e *Envelope) DecodePayload(v any) error {
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("kafka: decode payload: %w", err)
	}
	return nil
}

// newEventID — UUIDv4 без внешних зависимостей (crypto/rand).
func newEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // версия 4
	b[8] = (b[8] & 0x3f) | 0x80 // вариант RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
