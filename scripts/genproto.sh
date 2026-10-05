#!/usr/bin/env bash
#
# Генерация Go-кода из gRPC-контрактов (issue #2).
#
# Контракты:  proto/ads/v1/ads.proto, proto/analyzer/v1/analyzer.proto
# Результат:  pkg/proto/ads/v1/*.pb.go, pkg/proto/analyzer/v1/*.pb.go
# Запуск:     make proto   (или bash scripts/genproto.sh)
#
# Инструменты ставятся `go install` с зафиксированными версиями (ниже) в
# локальную папку .tools/bin (в .gitignore) — воспроизводимо и без прав
# системных путей. Используется buf (buf.build): он самодостаточен и НЕ
# требует установленного в системе protoc.
#
# Если когда-нибудь понадобится классический protoc:
#   macOS:   brew install protobuf
#   Debian:  apt install -y protobuf-compiler
#   и заменить вызов buf на:
#   protoc -I proto --go_out=pkg/proto --go_opt=paths=source_relative \
#     --go-grpc_out=pkg/proto --go-grpc_opt=paths=source_relative \
#     proto/ads/v1/ads.proto proto/analyzer/v1/analyzer.proto

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# --- Зафиксированные версии инструментов (воспроизводимая генерация) ------
BUF_VERSION="v1.73.0"                 # github.com/bufbuild/buf
PROTOC_GEN_GO_VERSION="v1.36.12"      # google.golang.org/protobuf
PROTOC_GEN_GO_GRPC_VERSION="v1.6.2"   # google.golang.org/grpc/cmd/protoc-gen-go-grpc

TOOLS_BIN="$ROOT/.tools/bin"
mkdir -p "$TOOLS_BIN"
export GOBIN="$TOOLS_BIN"
export PATH="$GOBIN:$PATH"

echo "==> Установка инструментов ($BUF_VERSION, $PROTOC_GEN_GO_VERSION, $PROTOC_GEN_GO_GRPC_VERSION)"
go install "github.com/bufbuild/buf/cmd/buf@$BUF_VERSION"
go install "google.golang.org/protobuf/cmd/protoc-gen-go@$PROTOC_GEN_GO_VERSION"
go install "google.golang.org/grpc/cmd/protoc-gen-go-grpc@$PROTOC_GEN_GO_GRPC_VERSION"

echo "==> Генерация (buf generate)"
(cd "$ROOT/proto" && "$TOOLS_BIN/buf" generate)

echo "==> Готово: $(find "$ROOT/pkg/proto" -name '*.pb.go' | sort)"
