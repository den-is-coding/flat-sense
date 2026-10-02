#!/bin/bash
set -e

SERVICES=(
  "auth-service"
  "ads-service"
  "parser-service"
  "analyzer-service"
  "staging-service"
  "notification-service"
  "payment-service"
  "proxy-manager"
  "api-gateway"
)

for svc in "${SERVICES[@]}"; do
  echo "📁 Creating service: $svc"
  mkdir -p "services/$svc"
  cd "services/$svc"

  # Инициализируем модуль с указанием версии Go 1.24
  go mod init "github.com/yourusername/real-estate-analyzer/$svc" 2>/dev/null || true
  go mod edit -go=1.24   # явно устанавливаем версию Go 1.24

  # Если go.sum существует, удаляем (пересоздадим позже, если нужно)
  rm -f go.sum
  touch go.sum

  # Минимальный main.go
  cat > main.go <<EOF
package main

import (
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	log.Println("$svc listening")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
EOF

  # .air.toml
  if [ -f "../../services/auth-service/.air.toml" ]; then
    cp "../../services/auth-service/.air.toml" .
  else
    cat > .air.toml <<'EOF'
root = "."
tmp_dir = "tmp"
[build]
  bin = "./tmp/main"
  cmd = "go build -o ./tmp/main ."
  delay = 1000
  exclude_dir = ["tmp", "vendor", "migrations", "pkg"]
  include_ext = ["go", "tpl", "tmpl", "html"]
  stop_on_error = true
  send_interrupt = true
  kill_delay = 500
EOF
  fi

  # Dockerfile (универсальный)
  cat > Dockerfile <<'EOF'
FROM golang:1.24-alpine3.20 AS builder
RUN apk add --no-cache ca-certificates tzdata
RUN addgroup -S appgroup && adduser -S appuser -G appgroup
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /server

FROM builder AS dev
RUN apk add --no-cache git make bash curl
RUN go install github.com/air-verse/air@latest
RUN go install github.com/go-delve/delve/cmd/dlv@latest
CMD ["air", "-c", ".air.toml"]

FROM scratch AS production
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group
COPY --from=builder --chown=appuser:appgroup --chmod=755 /server /server
USER appuser
EXPOSE 8080
CMD ["/server"]
EOF

  cd ../..
done

echo "✅ Все сервисы созданы с корректной версией Go 1.24!"