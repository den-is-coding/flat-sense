package kafka

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type testPayload struct {
	RequestID string `json:"request_id"`
	URL       string `json:"url"`
}

// TestEnvelopeRoundTrip — конверт переживает Marshal/DecodeEnvelope,
// payload декодируется обратно, event_id — валидный UUIDv4.
func TestEnvelopeRoundTrip(t *testing.T) {
	src, err := NewEnvelope(EventTypeParseRequest, testPayload{RequestID: "r-1", URL: "https://avito.ru/x"})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if !uuidV4.MatchString(src.EventID) {
		t.Fatalf("event_id не UUIDv4: %q", src.EventID)
	}
	if since := time.Since(src.OccurredAt); since < 0 || since > time.Minute {
		t.Fatalf("occurred_at не свежее UTC-время: %v", src.OccurredAt)
	}

	raw, err := src.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	dst, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if dst.EventID != src.EventID || dst.Type != src.Type || !dst.OccurredAt.Equal(src.OccurredAt) {
		t.Fatalf("конверт разошёлся: src=%+v dst=%+v", src, dst)
	}
	var p testPayload
	if err := dst.DecodePayload(&p); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if p.RequestID != "r-1" || p.URL != "https://avito.ru/x" {
		t.Fatalf("payload разошёлся: %+v", p)
	}
}

// TestEnvelopeJSONShape — поля конверта в snake_case, payload вложен.
func TestEnvelopeJSONShape(t *testing.T) {
	env, err := NewEnvelope(EventTypeParsedAd, testPayload{RequestID: "r-2"})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	raw, _ := env.Marshal()
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"event_id", "type", "occurred_at", "payload"} {
		if _, ok := generic[key]; !ok {
			t.Fatalf("нет поля %q в %s", key, raw)
		}
	}
}

// TestDecodeEnvelopeErrors — битый JSON и конверты без обязательных
// полей отвергаются (такие сообщения консьюмер уводит в DLQ).
func TestDecodeEnvelopeErrors(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"не JSON", `{{{{{`},
		{"без event_id", `{"type":"t","payload":{}}`},
		{"без type", `{"event_id":"abc","payload":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeEnvelope([]byte(tc.data)); err == nil {
				t.Fatalf("хотели ошибку для %s", tc.data)
			}
		})
	}
}

// TestNewEventIDUnique — event_id не повторяются (идемпотентность).
func TestNewEventIDUnique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id, err := newEventID()
		if err != nil {
			t.Fatalf("newEventID: %v", err)
		}
		if seen[id] {
			t.Fatalf("дубликат event_id: %s", id)
		}
		seen[id] = true
	}
}
