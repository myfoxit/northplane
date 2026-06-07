# Northplane build (SPEC §7.1: one static binary including the UI)
VERSION ?= 1.0.0-dev
LDFLAGS  = -ldflags "-X main.version=$(VERSION)"

.PHONY: all web build test race fmt docker clean

all: web build

web:
	cd web && npm ci --silent && npm run build
	/bin/rm -rf internal/web/dist
	cp -r web/dist internal/web/dist

build:
	go build $(LDFLAGS) -o bin/northplaned ./cmd/northplaned
	go build $(LDFLAGS) -o bin/np ./cmd/np
	go build $(LDFLAGS) -o bin/np-agent ./cmd/np-agent

test:
	go vet ./...
	go test ./...

# Race-enabled run of the whole suite (what CI runs).
race:
	go test -race ./...

fmt:
	gofmt -w cmd internal

docker:
	docker build --build-arg VERSION=$(VERSION) -t northplane:$(VERSION) .

clean:
	/bin/rm -rf bin web/dist internal/web/dist
	mkdir -p internal/web/dist && echo '<!doctype html><title>Northplane</title>' > internal/web/dist/index.html
