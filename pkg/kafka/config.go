package kafka

import (
	"log/slog"
	"os"
	"strings"
	"time"
)

// Config — адреса брокеров. Базовый конфиг берётся из окружения:
//
//	KAFKA_BROKERS — список через запятую (по умолчанию localhost:9092).
//	В docker-compose сервисы получают KAFKA_BROKERS=kafka:9092.
type Config struct {
	Brokers []string
}

const defaultBrokers = "localhost:9092"

// ConfigFromEnv — прочитать конфиг из переменных окружения.
func ConfigFromEnv() Config {
	return Config{Brokers: parseBrokers(os.Getenv("KAFKA_BROKERS"))}
}

func parseBrokers(v string) []string {
	if strings.TrimSpace(v) == "" {
		v = defaultBrokers
	}
	parts := strings.Split(v, ",")
	brokers := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			brokers = append(brokers, p)
		}
	}
	if len(brokers) == 0 {
		brokers = []string{defaultBrokers}
	}
	return brokers
}

// DefaultLogger — JSON-логгер пакета по умолчанию (stdout).
// Сервисы могут подменить своим через WithLogger.
func DefaultLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// Дефолты поведения, общие для продюсера и консьюмера.
const (
	defaultMaxAttempts = 4
	defaultBackoffBase = 200 * time.Millisecond
	defaultBackoffMax  = 5 * time.Second
	defaultDedupeTTL   = 10 * time.Minute
)

// defaultBackoff — экспоненциальный backoff: 200мс, 400мс, 800мс, ...,
// с потолком defaultBackoffMax.
func defaultBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := defaultBackoffBase << (attempt - 1)
	if d <= 0 || d > defaultBackoffMax {
		return defaultBackoffMax
	}
	return d
}
