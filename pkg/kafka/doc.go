// Package kafka — общая Kafka-инфраструктура MVP (issue #3):
// продюсер и консьюмер-группа поверх github.com/segmentio/kafka-go
// (MIT, чистый Go — совместимо со scratch-образами).
//
// Все сообщения ходят в JSON-конверте (Envelope):
//
//	{"event_id":"<uuid4>","type":"parse_request",
//	 "occurred_at":"2026-10-05T12:00:00Z","payload":{...}}
//
// Семантика — at-least-once: offset коммитится только после успешной
// обработки, идемпотентность обеспечивает дедупликация по event_id.
//
// # Продюсер
//
//	cfg := kafka.ConfigFromEnv() // KAFKA_BROKERS
//	p := kafka.NewProducer(cfg)
//	defer p.Close()
//	err := p.PublishParseRequest(ctx, kafka.ParseRequest{
//		RequestID: "r-123", URL: "https://www.avito.ru/...", Source: "avito",
//	})
//
// # Консьюмер с graceful shutdown (SIGTERM)
//
//	ctx, stop := signal.NotifyContext(context.Background(),
//		os.Interrupt, syscall.SIGTERM)
//	defer stop()
//
//	handler := func(ctx context.Context, m kafka.Message) error {
//		var req kafka.ParseRequest
//		if err := m.DecodePayload(&req); err != nil {
//			return err // битый payload уйдёт в DLQ
//		}
//		return parse(ctx, req)
//	}
//
//	consumer := kafka.NewConsumer(cfg, kafka.TopicParseRequests,
//		"parser", handler) // groupID — уникален для сервиса
//	if err := consumer.Run(ctx); err != nil { // вернётся после SIGTERM
//		log.Fatal(err)
//	}
//
// Run блокируется до отмены контекста: цикл чтения прерывается,
// обрабатываемое в этот момент сообщение коммитится (коммит делается
// с context.WithoutCancel), консьюмер выходит из группы — сервис
// можно останавливать без потери и без дублей вне TTL дедупликации.
//
// Ретраи: ошибка handler повторяется до maxAttempts (по умолчанию 4)
// с экспоненциальным backoff; после этого сообщение уходит в
// <topic>-dlq с заголовками x-dlq-source-topic/x-dlq-attempts/x-dlq-error.
// Сообщение с некорректным конвертом идёт в DLQ сразу.
//
// Топики MVP (создаются kafka-init в deploy/docker-compose.yml):
// parse-requests, parsed-ads и их DLQ-парные <topic>-dlq.
package kafka
