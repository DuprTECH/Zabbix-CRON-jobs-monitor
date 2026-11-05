VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
ARCHES  := amd64 arm64

.PHONY: all test build dist clean

all: test dist

test:
	go vet ./...
	go test ./...

build:
	go build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

# dist/<arch>/zbxntfy and dist/<arch>/zbxntfy-discovery (used by the Ansible role)
dist:
	@for a in $(ARCHES); do \
		echo "linux/$$a"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$a go build -ldflags "$(LDFLAGS)" -o dist/$$a/ ./cmd/... || exit 1; \
	done

clean:
	rm -rf bin dist
