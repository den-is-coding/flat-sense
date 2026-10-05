// Package config — конфигурация ads-service из окружения (issue #5).
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config — параметры запуска сервиса.
type Config struct {
	// DatabaseURL — DSN PostgreSQL (DATABASE_URL или собранный из DB_*).
	DatabaseURL string
	// GRPCPort — порт gRPC-сервера AdsService (GRPC_PORT, 50052).
	GRPCPort string
	// HTTPPort — порт health-эндпоинта (HTTP_PORT, 8080).
	HTTPPort string
	// KafkaBrokers — адреса брокеров. nil (KAFKA_BROKERS пуст) —
	// консьюмер не стартует, работает только gRPC.
	KafkaBrokers []string
	// KafkaGroupID — consumer group консьюмера parsed-ads.
	KafkaGroupID string
}

// FromEnv — прочитать конфиг из переменных окружения.
func FromEnv() Config {
	return Config{
		DatabaseURL:  databaseURL(),
		GRPCPort:     envOr("GRPC_PORT", "50052"),
		HTTPPort:     envOr("HTTP_PORT", "8080"),
		KafkaBrokers: splitList(os.Getenv("KAFKA_BROKERS")),
		KafkaGroupID: envOr("KAFKA_GROUP_ID", "ads-service"),
	}
}

// databaseURL — DATABASE_URL, а при отсутствии — DSN из частей
// DB_USER/DB_PASSWORD/DB_HOST/DB_PORT/DB_NAME/DB_SSLMODE (как у соседей
// по docker-compose, см. x-common-env).
func databaseURL() string {
	if v := strings.TrimSpace(os.Getenv("DATABASE_URL")); v != "" {
		return v
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		envOr("DB_USER", "analyzer"),
		envOr("DB_PASSWORD", "secret"),
		envOr("DB_HOST", "postgres"),
		envOr("DB_PORT", "5432"),
		envOr("DB_NAME", "analyzer"),
		envOr("DB_SSLMODE", "disable"),
	)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
