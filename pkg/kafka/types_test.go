package kafka

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	adsv1 "github.com/yourusername/real-estate-analyzer/pkg/proto/ads/v1"
)

// TestParseRequestJSONShape — поля в snake_case (контракт топика).
func TestParseRequestJSONShape(t *testing.T) {
	raw, err := json.Marshal(ParseRequest{RequestID: "r-1", URL: "https://x", Source: "avito"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	for _, key := range []string{`"request_id":"r-1"`, `"url":"https://x"`, `"source":"avito"`} {
		if !strings.Contains(s, key) {
			t.Fatalf("нет %s в %s", key, s)
		}
	}
}

// TestParsedAdJSONShape — ParsedAd несёт ads.v1.Ad, опциональные поля
// (rooms=0 у студии) переживают JSON-круг с присутствием.
func TestParsedAdJSONShape(t *testing.T) {
	src := ParsedAd{
		RequestID: "r-42",
		Ad: &adsv1.Ad{
			Id:        2794567890,
			Url:       "https://www.avito.ru/spb/kvartiry/studiya_123",
			Title:     "Студия 26 м2",
			Price:     5_500_000,
			Rooms:     proto.Int32(0),
			Studio:    true,
			TotalArea: proto.Float64(26.4),
		},
	}

	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"request_id":"r-42"`) {
		t.Fatalf("нет request_id в %s", raw)
	}

	var dst ParsedAd
	if err := json.Unmarshal(raw, &dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dst.RequestID != src.RequestID {
		t.Fatalf("request_id разошёлся: %q", dst.RequestID)
	}
	if !proto.Equal(src.Ad, dst.Ad) {
		t.Fatalf("Ad разошёлся:\n src=%+v\n dst=%+v", src.Ad, dst.Ad)
	}
	if dst.Ad.Rooms == nil || *dst.Ad.Rooms != 0 {
		t.Fatalf("rooms: хочу явный ноль с присутствием, got %v", dst.Ad.Rooms)
	}
}
