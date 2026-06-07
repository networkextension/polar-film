GOCACHE ?= /tmp/polar-go-cache
GO := env -u GOROOT GOCACHE=$(GOCACHE) go

.PHONY: build tidy vet test run

build:
	CGO_ENABLED=0 $(GO) build -o bin/film-svc ./cmd/film-svc

tidy:
	$(GO) mod tidy

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

run:
	$(GO) run ./cmd/film-svc
