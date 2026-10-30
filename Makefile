BINARY  := bin/gfluxy
PKG     := github.com/maxid/gfluxy
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG)/internal/version.version=$(VERSION) \
	-X $(PKG)/internal/version.commit=$(COMMIT) \
	-X $(PKG)/internal/version.buildDate=$(DATE)

.PHONY: all build run test fmt fmt-check vet lint ci clean

all: build

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/gfluxy

run: build
	./$(BINARY) -version

test:
	go test ./...

fmt:
	gofmt -l -w .

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

lint: fmt-check vet

ci: fmt-check vet test build
	./$(BINARY) -version

clean:
	rm -rf bin
