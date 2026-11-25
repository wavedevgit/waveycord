GO ?= go
DSN ?= postgres://waveycord:waveycord@127.0.0.1:5432/waveycord_test?sslmode=disable

.PHONY: build test test-pg fmt vet run cluster compose clean

build:
	$(GO) build ./...

test:
	$(GO) test ./... -timeout 180s

test-pg:
	WAVEYCORD_TEST_DSN='$(DSN)' $(GO) test ./... -timeout 180s

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...

run:
	WAVEYCORD_DB=$${WAVEYCORD_DB:-./waveycord.db} $(GO) run ./cmd/waveycord

cluster:
	@echo "requires postgres; see docker-compose.yml"

compose:
	docker compose up --build

clean:
	rm -f waveycord.db waveycord.db-shm waveycord.db-wal
	rm -rf uploads
