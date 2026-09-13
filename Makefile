BINARY  := zealish-router
PKG     := ./...
BIN_DIR := bin
# Derived from the nearest tag so a local build never claims a stale version.
# goreleaser and the image build stamp their own value.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

PLATFORMS := linux/amd64,linux/arm64
comma     := ,

.PHONY: all build release run dev test race lint vet fmt tidy clean docker docker-multiarch snapshot

all: build

build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) ./cmd/server

# release cross-compiles the published target matrix into bin/.
release:
	@mkdir -p $(BIN_DIR)
	@for platform in $(subst $(comma), ,$(PLATFORMS)); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags '$(LDFLAGS)' \
			-o $(BIN_DIR)/$(BINARY)_$${os}_$${arch} ./cmd/server || exit 1; \
	done

run:
	go run ./cmd/server -config config.yaml serve

# dev runs the router and the dashboard together; Ctrl-C stops both.
dev:
	@go run ./cmd/server -config config.yaml serve & router=$$!; \
	npm --prefix apps/dashboard run dev & dash=$$!; \
	trap 'kill $$router $$dash 2>/dev/null' INT TERM; \
	wait $$router $$dash

test:
	go test $(PKG)

race:
	go test -race $(PKG)

lint:
	golangci-lint run $(PKG)

vet:
	go vet $(PKG)

fmt:
	gofmt -l -w .

tidy:
	go mod tidy

clean:
	rm -rf $(BIN_DIR)

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):$(VERSION) .

# Requires a buildx builder; --push is needed because the local image store
# cannot hold a multi-platform manifest.
docker-multiarch:
	docker buildx build --platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) -t $(BINARY):$(VERSION) .

snapshot:
	goreleaser release --snapshot --clean
