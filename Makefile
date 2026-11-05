SHELL := /bin/sh
BINARY := cardword
VERSION ?= v1.0.0
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
  -X github.com/yiluoiro/card-word/internal/version.Version=$(VERSION) \
  -X github.com/yiluoiro/card-word/internal/version.Commit=$(COMMIT) \
  -X github.com/yiluoiro/card-word/internal/version.Date=$(DATE)

.PHONY: all build sign-tool test race vet run clean docker docker-run sign-keygen

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/cardword

sign-tool:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o cardword-sign ./cmd/cardword-sign

test:
	go test ./... -count=1

race:
	go test ./... -race -count=1

vet:
	go vet ./...

run: build
	./$(BINARY) --addr 127.0.0.1:8080 --data ./tmpdata --env development --base-url http://127.0.0.1:8080

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t cardword:$(VERSION) .

docker-run:
	docker compose up -d --build

sign-keygen: sign-tool
	./cardword-sign keygen --out ./keys

clean:
	rm -f $(BINARY) cardword-sign
	rm -rf dist
