.PHONY: deps web build linux-amd64 linux-arm64 check test demo

GO ?= go
PNPM ?= pnpm

deps:
	$(PNPM) --dir web install --frozen-lockfile
	$(GO) mod download

web:
	$(PNPM) --dir web build

build: web
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -p 2 -trimpath -o bin/container-proxy ./cmd/dashboard

linux-amd64: web
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -p 2 -trimpath -o bin/container-proxy-linux-amd64 ./cmd/dashboard

linux-arm64: web
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -p 2 -trimpath -o bin/container-proxy-linux-arm64 ./cmd/dashboard

check: web
	$(PNPM) --dir web check
	$(PNPM) --dir web test
	$(GO) test -p 2 ./...
	$(GO) vet -p 2 ./...

test: check

demo: build
	./bin/container-proxy -demo -listen :8080 -state-dir .local/demo
