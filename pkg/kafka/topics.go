package kafka

// Топики MVP-потока (PROJECT_NOTES.md 2.3, issue #3). Создаются
// kafka-init в deploy/docker-compose.yml; партиций — 3, RF — 1.
const (
	// TopicParseRequests — «распарси объявление» (parser-service консьюмер).
	TopicParseRequests = "parse-requests"
	// TopicParsedAds — «объявление распарсено и сохранено»
	// (analyzer-service консьюмер).
	TopicParsedAds = "parsed-ads"
)

// DLQTopic — имя очереди мёртвых писем для топика: <topic>-dlq.
// Сообщение попадает туда после N неудачных попыток обработки.
func DLQTopic(topic string) string { return topic + "-dlq" }
