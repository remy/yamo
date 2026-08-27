BIN     := tagmgr
PKG     := ./cmd/tagmgr
DIST    := dist
LDFLAGS := -s -w

# Static, dependency-free binaries. CGO is off so the result runs on any
# kernel of the right architecture regardless of the NAS's libc.
GOFLAGS := -trimpath -ldflags="$(LDFLAGS)"
export CGO_ENABLED = 0

.PHONY: all build test vet fmt bench nas clean install

all: build

build:
	go build $(GOFLAGS) -o $(DIST)/$(BIN) $(PKG)

# nas cross-compiles for both architectures UGREEN ships.
nas:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o $(DIST)/$(BIN)-linux-amd64 $(PKG)
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(DIST)/$(BIN)-linux-arm64 $(PKG)

install:
	go install $(GOFLAGS) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

bench:
	go test ./internal/catalog/ -bench . -benchtime 50x -run XXX

clean:
	rm -rf $(DIST)
