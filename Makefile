BINARY   := thornotes
MODULE   := github.com/th0rn0/thornotes
MAIN     := ./cmd/thornotes
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS  := -ldflags "-s -w -X main.Version=$(VERSION)"

.PHONY: build test run clean fmt vet lint release

build:
	go build $(LDFLAGS) -o $(BINARY) $(MAIN)

run: build
	./$(BINARY)

test:
	go test ./... -count=1 -timeout 60s

test-verbose:
	go test ./... -count=1 -timeout 60s -v

fmt:
	gofmt -w .

vet:
	go vet ./...

# Cross-platform release builds.
release:
	GOOS=linux  GOARCH=amd64  go build $(LDFLAGS) -o dist/$(BINARY)-linux-amd64   $(MAIN)
	GOOS=linux  GOARCH=arm64  go build $(LDFLAGS) -o dist/$(BINARY)-linux-arm64   $(MAIN)
	GOOS=darwin GOARCH=arm64  go build $(LDFLAGS) -o dist/$(BINARY)-darwin-arm64  $(MAIN)
	GOOS=darwin GOARCH=amd64  go build $(LDFLAGS) -o dist/$(BINARY)-darwin-amd64  $(MAIN)
	ls -lh dist/

clean:
	rm -f $(BINARY)
	rm -rf dist/

# Run with development defaults.
dev:
	go run $(MAIN) --addr :8080 --db thornotes-dev.db --notes-root notes-dev --allow-registration
