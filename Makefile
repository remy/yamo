BIN     := tagmgr
PKG     := ./cmd/tagmgr
DIST    := dist
LDFLAGS := -s -w

# Static, dependency-free binaries. CGO is off so the result runs on any
# kernel of the right architecture regardless of the NAS's libc.
GOFLAGS := -trimpath -ldflags="$(LDFLAGS)"
export CGO_ENABLED = 0

# Where a release lands. The NAS is x86-64, and the share is mounted here on
# the Mac; the binary is copied over SMB rather than scp'd.
RELEASE := /Volumes/Media/tagmgr

.PHONY: all build test vet fmt bench nas release clean install

all: build

build:
	go build $(GOFLAGS) -o $(DIST)/$(BIN) $(PKG)

# nas cross-compiles for both architectures UGREEN ships.
nas:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o $(DIST)/$(BIN)-linux-amd64 $(PKG)
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(DIST)/$(BIN)-linux-arm64 $(PKG)

# release copies the amd64 build onto the NAS share.
#
# chmod over SMB only works if the mount honours it; the check afterwards is
# there because a binary that arrives without the execute bit fails with
# "permission denied" on the NAS and looks like something worse.
release: nas
	cp $(DIST)/$(BIN)-linux-amd64 $(RELEASE)
	chmod +x $(RELEASE)
	@test -x $(RELEASE) && echo "released $$(ls -l $(RELEASE) | awk '{print $$5}') bytes to $(RELEASE)" \
		|| echo "WARNING: $(RELEASE) is not executable; chmod it on the NAS itself"

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
