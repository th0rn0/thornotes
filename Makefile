BINARY   := thornotes
MODULE   := github.com/th0rn0/thornotes
MAIN     := ./cmd/thornotes
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS  := -ldflags "-s -w -X main.Version=$(VERSION)"

.PHONY: build test run clean fmt vet lint release build-cm6 desktop desktop-dist desktop-wails desktop-wails-dist android android-release test-db-up test-db-down test-with-db

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

build-cm6:
	cd web/cm6-bundle && bun install --frozen-lockfile && bun build index.js --bundle --format=iife --target=browser --outfile=../static/js/vendor/codemirror6.min.js --minify

# ── Desktop app (Electron) ────────────────────────────────────────────────────
desktop:
	cd desktop && npm install && npm start

desktop-dist:
	cd desktop && npm install && npm run dist:$(or $(PLATFORM),linux)

# ── Desktop app (Wails) ───────────────────────────────────────────────────────
# Requires: wails CLI — go install github.com/wailsapp/wails/v2/cmd/wails@latest
# Linux deps: libgtk-3-dev libwebkit2gtk-4.0-dev libayatana-appindicator3-dev
desktop-wails:
	cd desktop-wails && wails dev

desktop-wails-dist:
	cd desktop-wails && wails build -o thornotes-wails$(or $(WAILS_EXT),)

# ── Android APK ───────────────────────────────────────────────────────────────
# Requires: Android Studio or Android SDK + JDK 17+
# First time: cd android && gradle wrapper  (generates gradlew JAR)
android:
	cd android && ./gradlew assembleDebug
	@echo "APK: android/app/build/outputs/apk/debug/app-debug.apk"

android-release:
	cd android && ./gradlew assembleRelease
	@echo "APK: android/app/build/outputs/apk/release/app-release-unsigned.apk"

# ── Database (local testing via Docker Compose) ───────────────────────────────
TEST_DSN := thornotes:thornotes@tcp(127.0.0.1:3306)/thornotes_test?parseTime=true

test-db-up:
	docker compose up -d db
	@echo "Waiting for MariaDB to be healthy..."
	@until docker compose exec db healthcheck.sh --connect --innodb_initialized 2>/dev/null; do sleep 1; done
	@echo "MariaDB ready."

test-db-down:
	docker compose down

test-with-db:
	THORNOTES_TEST_MYSQL_DSN="$(TEST_DSN)" go test -race ./... -count=1 -timeout 120s

clean:
	rm -f $(BINARY)
	rm -rf dist/

# Run with development defaults.
dev:
	go run $(MAIN) --addr :8080 --db thornotes-dev.db --notes-root notes-dev --allow-registration
