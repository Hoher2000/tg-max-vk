.PHONY: run dev test build lint auth clean

# Запуск в обычном режиме
run:
	go run ./cmd/tg-max-vk run -c config/config.yaml

# Запуск с горячей перезагрузкой (Air)
dev:
	air

# Запуск тестов
test:
	go test -v ./...

# Сборка бинарников
build:
	go build -o bin/tg-max-vk ./cmd/tg-max-vk
	go build -o bin/tg-max-vk-auth ./cmd/tg-max-vk-auth

# Проверка кода (линтер)
lint:
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; fi

# Авторизация в Telegram
auth:
	go run ./cmd/tg-max-vk-auth

# Очистка
clean:
	rm -rf bin/ tmp/ data/media/*