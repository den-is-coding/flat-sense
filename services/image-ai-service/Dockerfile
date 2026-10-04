FROM golang:1.25-alpine AS builder
RUN apk add --no-cache ca-certificates tzdata
RUN addgroup -S appgroup && adduser -S appuser -G appgroup
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /server

FROM builder AS dev
RUN apk add --no-cache git make bash curl
RUN go install github.com/cosmtrek/air@v1.52.0
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
