# pkg/kafka — Kafka-инфраструктура MVP (issue #3)

Общий продюсер и консьюмер-группа flat-sense поверх
[segmentio/kafka-go](https://github.com/segmentio/kafka-go) (MIT,
чистый Go, без cgo — собирается в scratch-образы).

## Формат сообщений

Любое сообщение топика — JSON-конверт `Envelope`:

```json
{
  "event_id": "8f3c1a2e-...-...",
  "type": "parse_request",
  "occurred_at": "2026-10-05T12:00:00Z",
  "payload": { "request_id": "r-123", "url": "https://...", "source": "avito" }
}
```

- `event_id` — UUIDv4, генерирует продюсер; ключ идемпотентности.
- `payload` — типизированное сообщение (`ParseRequest` / `ParsedAd`).

Типы сообщений MVP:

| Тип | Константа | Топик | Payload |
|---|---|---|---|
| задание парсеру | `kafka.EventTypeParseRequest` | `parse-requests` | `kafka.ParseRequest{request_id, url, source}` |
| результат парсинга | `kafka.EventTypeParsedAd` | `parsed-ads` | `kafka.ParsedAd{request_id, ad: ads.v1.Ad}` |

Топики (вместе с парными `<topic>-dlq`) создаёт `kafka-init`
в `deploy/docker-compose.yml`: 3 партиции, RF=1.
Не-MVP топики (`image-analysis-requests`, `analysis-results`,
`email-notifications`) будут добавлены на своих этапах.

## Продюсер

```go
cfg := kafka.ConfigFromEnv() // KAFKA_BROKERS, по умолчанию localhost:9092
p := kafka.NewProducer(cfg)
defer p.Close()

err := p.PublishParseRequest(ctx, kafka.ParseRequest{
    RequestID: "r-123",
    URL:       "https://www.avito.ru/moskva/kvartiry/...",
    Source:    "avito",
})
```

- Синхронная отправка: `WriteMessages` ждёт подтверждения брокера
  (`WaitForAll`), при ошибке — до 4 попыток с экспоненциальным backoff
  (настраивается `WithProducerRetries`).
- Балансировщик `Hash` по ключу = `request_id`: порядок сообщений
  одного запроса сохраняется внутри партиции.

## Консьюмер

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

handler := func(ctx context.Context, m kafka.Message) error {
    var ad kafka.ParsedAd
    if err := m.DecodePayload(&ad); err != nil {
        return err // битый payload уйдёт в DLQ
    }
    return index(ctx, ad)
}

c := kafka.NewConsumer(cfg, kafka.TopicParsedAds, "analyzer", handler,
    kafka.WithLogger(appLogger)) // JSON-логи приложения
err := c.Run(ctx) // вернётся (nil) после SIGTERM
```

- **Consumer group**: offset коммитится только после успешной
  обработки (at-least-once). `groupID` — свой у каждого сервиса.
- **Graceful shutdown**: `Run` блокируется до отмены контекста
  (SIGTERM через `signal.NotifyContext`); обрабатываемое сообщение
  коммитится (коммит переживает отмену — `context.WithoutCancel`),
  читатель закрывается, консьюмер выходит из группы.
- **Ретраи**: ошибка handler повторяется до `WithMaxAttempts` (4 по
  умолчанию) с backoff; после — сообщение уходит в `<topic>-dlq` с
  заголовками `x-dlq-source-topic`, `x-dlq-attempts`, `x-dlq-error`.
  Сообщение с некорректным конвертом идёт в DLQ сразу, не крутя ретраи.
- **Идемпотентность**: обработанные `event_id` помнятся в памяти
  (`WithDedupeTTL`, по умолчанию 10 минут); дубликаты пропускаются.

## Конфигурация из окружения

| Переменная | По умолчанию | Смысл |
|---|---|---|
| `KAFKA_BROKERS` | `localhost:9092` | адреса брокеров через запятую |

Остальные параметры (число попыток, backoff, TTL дедупликации, логгер)
— опции `NewProducer`/`NewConsumer`.

## Ограничения MVP

- Дедупликация по `event_id` — **in-memory**: при нескольких репликах
  одного сервиса дубликаты между репликами не ловятся (нужен Redis);
  внутри одной реплики ловятся.
- Ровно-один-раз (exactly-once) не гарантируется — это at-least-once;
  обработчики должны быть идемпотентными (upsert по `request_id`).
- DLQ-топики для MVP создаются в `kafka-init`; мониторинг DLQ — ручной
  (kafka-ui на :8090).

## Тесты

```bash
go test ./kafka/                        # unit: конверт, типы, ретраи, DLQ, дедуп
go test -tags integration ./kafka/      # интеграционные: нужен Kafka на localhost:9092
```
