# herdr-tg build, test and lint entrypoints.
# `make` builds bin/herdr-tg for the host; `make lint` runs gofmt, go vet,
# staticcheck, the forbidden-import gate and the cross-compile check.

.DEFAULT_GOAL := build

VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
STATICCHECK ?= $(HOME)/go/bin/staticcheck
LDFLAGS     := -s -w -X main.version=$(VERSION)

.PHONY: build test lint crosscheck clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/herdr-tg ./cmd/herdr-tg

test:
	go test -race ./...

lint:
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "gofmt: needs formatting:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	$(STATICCHECK) ./...
	sh scripts/check-imports.sh
	$(MAKE) crosscheck

# Stub until go-winio lands with the Herdr adapter: host build only.
# Replaced by scripts/crosscheck.sh (all release targets) in the same milestone.
crosscheck:
	CGO_ENABLED=0 go build -o /dev/null ./...

clean:
	rm -rf bin
