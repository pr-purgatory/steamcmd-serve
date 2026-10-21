.PHONY: build test vet lint-api

build:
	go build -o bin/controller ./cmd/controller

test:
	go test ./...

vet:
	go vet ./...

lint-api:
	npx --yes @redocly/cli@2 lint api/openapi.yaml

# Integration tests against a container engine (Docker or Podman via DOCKER_HOST).
.PHONY: it-image it
it-image:
	podman build -t localhost/steamcmd-serve-fake-runtime:test internal/instance/testdata/fake-runtime

it: it-image
	SS_IT_IMAGE=localhost/steamcmd-serve-fake-runtime:test go test -count=1 -run Integration -v ./internal/instance/
