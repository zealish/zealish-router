BINARY  := zealish-router
PKG     := ./...
BIN_DIR := bin
VERSION ?= 0.1.0
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build run test vet fmt tidy clean docker

all: build

build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) ./cmd/server

run:
	go run ./cmd/server -config config.yaml serve

test:
	go test $(PKG)

vet:
	go vet $(PKG)

fmt:
	gofmt -l -w .

tidy:
	go mod tidy

clean:
	rm -rf $(BIN_DIR)

docker:
	docker build -t $(BINARY):$(VERSION) .
