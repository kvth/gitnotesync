BIN     := gitnotesync
PREFIX  ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/kvth/gitnotesync/cmd.Version=$(VERSION)

.PHONY: build test vet fmt install clean

build:
	go build -ldflags '$(LDFLAGS)' -o $(BIN) .

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

install: build
	install -Dm755 $(BIN) $(PREFIX)/bin/$(BIN)

clean:
	rm -f $(BIN)
