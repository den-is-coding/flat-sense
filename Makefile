# ============================================================
# Makefile для проверки инфраструктуры и микросервисов
# ============================================================

COMPOSE_FILE := deploy/docker-compose.yml
ENV_FILE := deploy/.env

# Цветной вывод (опционально)
GREEN := \033[0;32m
RED := \033[0;31m
YELLOW := \033[0;33m
NC := \033[0m # No Color

.PHONY: health ping-postgres ping-redis ping-minio ping-kafka ping-microservices

# ------------------------------------------------------------
# Комплексная проверка всех компонентов
# ------------------------------------------------------------
health: ping-postgres ping-redis ping-minio ping-kafka ping-microservices
	@echo ""
	@echo "${GREEN}✅ Все проверки пройдены.${NC}"

# ------------------------------------------------------------
# Проверка PostgreSQL
# ------------------------------------------------------------
ping-postgres:
	@echo "${YELLOW}🔍 Проверка PostgreSQL...${NC}"
	@if docker compose -f $(COMPOSE_FILE) exec -T postgres pg_isready -U $(shell grep POSTGRES_USER $(ENV_FILE) | cut -d '=' -f2) > /dev/null 2>&1; then \
		echo "${GREEN}✅ PostgreSQL работает${NC}"; \
	else \
		echo "${RED}❌ PostgreSQL недоступен${NC}"; \
		exit 1; \
	fi

# ------------------------------------------------------------
# Проверка Redis
# ------------------------------------------------------------
ping-redis:
	@echo "${YELLOW}🔍 Проверка Redis...${NC}"
	@if docker compose -f $(COMPOSE_FILE) exec -T redis redis-cli -a $(shell grep REDIS_PASSWORD $(ENV_FILE) | cut -d '=' -f2) ping | grep -q PONG; then \
		echo "${GREEN}✅ Redis работает${NC}"; \
	else \
		echo "${RED}❌ Redis недоступен${NC}"; \
		exit 1; \
	fi

# ------------------------------------------------------------
# Проверка MinIO (через внутренний healthcheck)
# ------------------------------------------------------------
ping-minio:
	@echo "${YELLOW}🔍 Проверка MinIO...${NC}"
	@if docker compose -f $(COMPOSE_FILE) exec -T minio curl -s -f http://localhost:9000/minio/health/live > /dev/null 2>&1; then \
		echo "${GREEN}✅ MinIO работает${NC}"; \
	else \
		echo "${RED}❌ MinIO недоступен${NC}"; \
		exit 1; \
	fi

# ------------------------------------------------------------
# Проверка Kafka (список топиков)
# ------------------------------------------------------------
ping-kafka:
	@echo "${YELLOW}🔍 Проверка Kafka...${NC}"
	@if docker compose -f $(COMPOSE_FILE) exec -T kafka kafka-topics --bootstrap-server localhost:9092 --list > /dev/null 2>&1; then \
		echo "${GREEN}✅ Kafka работает${NC}"; \
	else \
		echo "${RED}❌ Kafka недоступен${NC}"; \
		exit 1; \
	fi

# ------------------------------------------------------------
# Проверка микросервисов (HTTP и gRPC)
# ------------------------------------------------------------
ping-microservices:
	@echo "${YELLOW}🔍 Проверка микросервисов...${NC}"
	@for svc in auth-service ads-service parser-service analyzer-service staging-service notification-service payment-service proxy-manager api-gateway; do \
		if docker compose -f $(COMPOSE_FILE) ps -q $$svc > /dev/null 2>&1; then \
			HTTP_PORT=""; \
			GRPC_PORT=""; \
			case $$svc in \
				auth-service) HTTP_PORT=8080; GRPC_PORT=50051;; \
				ads-service) HTTP_PORT=8080; GRPC_PORT=50052;; \
				staging-service) HTTP_PORT=8080; GRPC_PORT=50053;; \
				notification-service) HTTP_PORT=8080; GRPC_PORT=50054;; \
				payment-service) HTTP_PORT=8080; GRPC_PORT=50055;; \
				proxy-manager) HTTP_PORT=8001; GRPC_PORT="";; \
				api-gateway) HTTP_PORT=80; GRPC_PORT="";; \
				*) HTTP_PORT=""; GRPC_PORT="";; \
			esac; \
			if [ -n "$$HTTP_PORT" ]; then \
				if curl -s -f http://localhost:$$HTTP_PORT/health > /dev/null 2>&1; then \
					echo "${GREEN}✅ $$svc (HTTP $$HTTP_PORT) работает${NC}"; \
				else \
					echo "${RED}❌ $$svc (HTTP $$HTTP_PORT) не отвечает${NC}"; \
				fi \
			fi; \
			if [ -n "$$GRPC_PORT" ]; then \
				if nc -z localhost $$GRPC_PORT 2>/dev/null; then \
					echo "${GREEN}✅ $$svc (gRPC $$GRPC_PORT) слушает${NC}"; \
				else \
					echo "${RED}❌ $$svc (gRPC $$GRPC_PORT) не слушает${NC}"; \
				fi \
			fi; \
			if [ -z "$$HTTP_PORT" ] && [ -z "$$GRPC_PORT" ]; then \
				echo "${YELLOW}⚠️ $$svc — нет портов для проверки${NC}"; \
			fi; \
		else \
			echo "${YELLOW}⚠️ $$svc не запущен${NC}"; \
		fi; \
	done

# ------------------------------------------------------------
# Дополнительно: быстрая проверка, что все контейнеры запущены
# ------------------------------------------------------------
ps:
	@docker compose -f $(COMPOSE_FILE) ps

# ------------------------------------------------------------
# Запуск миграций (если ещё не накатили)
# ------------------------------------------------------------
migrate:
	@docker compose -f $(COMPOSE_FILE) run --rm migrate up