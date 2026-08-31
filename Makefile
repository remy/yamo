BIN     := yamo
PKG     := ./cmd/yamo
DIST    := dist
LDFLAGS := -s -w

# Static, dependency-free binaries. CGO is off so the result runs on any
# kernel of the right architecture regardless of the NAS's libc.
GOFLAGS := -trimpath -ldflags="$(LDFLAGS)"
export CGO_ENABLED = 0

# Where a release lands. The NAS is x86-64, and the share is mounted here on
# the Mac; the binary is copied over SMB rather than scp'd.
RELEASE := /Volumes/Media/yamo

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
# It writes a temporary name and renames over the target, because overwriting
# a binary that is currently running fails outright. A rename swaps the
# directory entry instead, so a running server keeps the file it started from
# and the next start picks up the new one — no need to stop the server to
# deploy.
#
# chmod over SMB only works if the mount honours it, so the execute bit is
# checked rather than assumed: a binary that arrives without it fails on the
# NAS as "permission denied", which looks like something far worse.
release: nas
	cp $(DIST)/$(BIN)-linux-amd64 $(RELEASE).new
	chmod +x $(RELEASE).new
	mv -f $(RELEASE).new $(RELEASE)
	@test -x $(RELEASE) \
		&& echo "released $$(ls -l $(RELEASE) | awk '{print $$5}') bytes to $(RELEASE)" \
		&& echo "the running server keeps the old binary; restart it to pick this up" \
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
