# --- Build stage ---
FROM golang:1.27-alpine AS builder

WORKDIR /src

# Кэшируем зависимости.
COPY go.mod go.sum ./
RUN go mod download

# Копируем исходники и собираем.
COPY . .

ARG VERSION=dev

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/tg-max-vk ./cmd/tg-max-vk

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/tg-max-vk-auth ./cmd/tg-max-vk-auth

# --- Runtime stage ---
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1000 bridge && \
    mkdir -p /app/data /app/configs && \
    chown -R bridge:bridge /app

WORKDIR /app

COPY --from=builder /out/tg-max-vk /usr/local/bin/tg-max-vk
COPY --from=builder /out/tg-max-vk-auth /usr/local/bin/tg-max-vk-auth

USER bridge

ENTRYPOINT ["tg-max-vk"]
CMD ["run", "-c", "/app/configs/config.yaml"]