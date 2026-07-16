.PHONY: build test test-race coverage vet verify e2e compose-config

build:
	go build ./cmd/...

test:
	go test -count=1 ./...

test-race:
	go test -race -count=1 ./...

coverage:
	go test -count=1 -covermode=atomic -coverprofile=coverage.out ./internal/...
	go run ./tools/coveragecheck -profile coverage.out -minimum 90

vet:
	go vet ./...

compose-config:
	docker compose -f deploy/compose.yaml config --quiet
	docker compose -f deploy/compose.yaml -f deploy/compose.e2e.yaml config --quiet

e2e:
	AI_GATEWAY_E2E=1 go test -tags=e2e -count=1 -timeout=5m ./test/e2e

verify: build test test-race coverage vet compose-config
