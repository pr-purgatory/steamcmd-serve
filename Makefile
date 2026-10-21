.PHONY: build test vet lint-api

build:
	go build -o bin/controller ./cmd/controller

test:
	go test ./...

vet:
	go vet ./...

lint-api:
	npx --yes @redocly/cli@2 lint api/openapi.yaml
