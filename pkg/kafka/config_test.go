package kafka

import (
	"log/slog"
	"reflect"
	"testing"
	"time"
)

func TestConfigFromEnv(t *testing.T) {
	t.Run("из KAFKA_BROKERS, с пробелами и пустыми элементами", func(t *testing.T) {
		t.Setenv("KAFKA_BROKERS", " kafka:9092, b2:9092 ,,")
		cfg := ConfigFromEnv()
		if want := []string{"kafka:9092", "b2:9092"}; !reflect.DeepEqual(cfg.Brokers, want) {
			t.Fatalf("brokers = %v, хочу %v", cfg.Brokers, want)
		}
	})

	t.Run("по умолчанию localhost:9092", func(t *testing.T) {
		t.Setenv("KAFKA_BROKERS", "")
		cfg := ConfigFromEnv()
		if want := []string{"localhost:9092"}; !reflect.DeepEqual(cfg.Brokers, want) {
			t.Fatalf("brokers = %v, хочу %v", cfg.Brokers, want)
		}
	})
}

func TestDLQTopic(t *testing.T) {
	if got, want := DLQTopic(TopicParseRequests), "parse-requests-dlq"; got != want {
		t.Fatalf("DLQTopic = %q, хочу %q", got, want)
	}
}

func TestDefaultBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
		{5, 3200 * time.Millisecond},
		{6, 5 * time.Second},  // потолок
		{20, 5 * time.Second}, // переполнение сдвига ловится потолком
	}
	for _, tc := range cases {
		if got := defaultBackoff(tc.attempt); got != tc.want {
			t.Fatalf("defaultBackoff(%d) = %v, хочу %v", tc.attempt, got, tc.want)
		}
	}
}

func TestDedupeSet(t *testing.T) {
	s := newDedupeSet(50 * time.Millisecond)
	if !s.mark("a") {
		t.Fatal("первый mark(a) должен быть новым")
	}
	if s.mark("a") {
		t.Fatal("второй mark(a) — дубликат")
	}
	time.Sleep(60 * time.Millisecond)
	if !s.mark("a") {
		t.Fatal("после TTL mark(a) должен снова пропускать")
	}
}

func TestDefaultLoggerIsJSON(t *testing.T) {
	// JSON-логи — контракт пакета: логгер по умолчанию должен быть
	// JSON-хендлером (сервисы могут подменить своим через WithLogger).
	if _, ok := DefaultLogger().Handler().(*slog.JSONHandler); !ok {
		t.Fatalf("логгер по умолчанию не JSON: %T", DefaultLogger().Handler())
	}
}
