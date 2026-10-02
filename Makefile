BIN     := yamo
PKG     := ./cmd/yamo
DIST    := dist

# VERSION is the tag if HEAD has one and a description of the distance from
# the last tag otherwise, so a local build never claims to be a release. It
# can be overridden (make dist VERSION=1.2.3) for a build from an export with
# no git directory, where `git describe` has nothing to go on.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)

# -s -w drop the symbol table and DWARF, which is about a third of the binary
# and nothing a user of a release needs. The two -X stamps are what make a
# downloaded binary able to say what it is; see cmd/yamo/version.go for what
# happens when they are absent.
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# Static, dependency-free binaries. CGO is off so the result runs on any
# kernel of the right architecture regardless of the NAS's libc.
GOFLAGS := -trimpath -ldflags="$(LDFLAGS)"
export CGO_ENABLED = 0

# The platforms a release ships, as os/arch pairs. linux covers the NAS this
# was written for, darwin the machine it is developed on, and windows/amd64
# is there because the terminal browser works in Windows Terminal — Bubble
# Tea reads a Windows console directly rather than needing a pty.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

# Where a release lands. The NAS is x86-64, and the share is mounted here on
# the Mac; the binary is copied over SMB rather than scp'd.
RELEASE := /Volumes/Media/yamo

.PHONY: all build test vet fmt bench nas dist release clean install version

all: build

build:
	go build $(GOFLAGS) -o $(DIST)/$(BIN) $(PKG)

# nas cross-compiles for both architectures UGREEN ships.
nas:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o $(DIST)/$(BIN)-linux-amd64 $(PKG)
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(DIST)/$(BIN)-linux-arm64 $(PKG)

# version prints what a build made right now would report, which is the one
# thing worth checking before cutting a tag.
version:
	@echo "$(VERSION) ($(COMMIT))"

# dist builds every release platform and packages each one: one archive per
# platform holding the binary under its plain name, plus the README, and a
# single checksums file over the lot.
#
# release.yml runs this target rather than carrying its own build commands, so
# the archives a release ships and the archives this produces cannot drift. If
# `make dist` is broken, the release is broken, and that is visible here
# before a tag is pushed.
#
# "The same archives" means the same contents, not the same bytes: tar and zip
# record modification times, so two runs produce different checksums from
# identical binaries. Compare the unpacked executables, not the archives.
#
# Archives are named with the bare version — no leading "v" — because that is
# how the Docker tags already read.
#
# The recipe clears archives from earlier runs before building, so dist/ never
# holds two versions at once and a glob over it cannot pick up a stale one.
# It is done by recursing rather than with an order-only prerequisite because
# under `make -j` nothing guarantees a sibling prerequisite runs before the
# archives, and a clean step racing the build would delete what it just made.
dist:
	@rm -f $(DIST)/$(BIN)_*.tar.gz $(DIST)/$(BIN)_*.zip $(DIST)/checksums.txt
	@$(MAKE) --no-print-directory $(DIST)/checksums.txt

DISTVER := $(patsubst v%,%,$(VERSION))
ARCHIVES := $(foreach p,$(PLATFORMS),$(DIST)/$(BIN)_$(DISTVER)_$(subst /,_,$(p))$(if $(filter windows,$(firstword $(subst /, ,$(p)))),.zip,.tar.gz))

# sha256sum is GNU, shasum is what macOS ships; whichever exists is used, and
# both write the same format, so a checksums.txt from either verifies with
# either.
$(DIST)/checksums.txt: $(ARCHIVES)
	@cd $(DIST) && (command -v sha256sum >/dev/null && sha256sum $(notdir $(ARCHIVES)) || shasum -a 256 $(notdir $(ARCHIVES))) > checksums.txt
	@echo "--- $(DIST)/checksums.txt"
	@cat $@

# One rule per archive kind. The staging directory is per-platform so that
# two of these running under `make -j` cannot overwrite each other's binary,
# and the binary inside the archive is called plain "yamo": the platform is
# in the archive's name, and nobody wants to rename a file after unpacking.
define archive_rule
$(DIST)/$(BIN)_$(DISTVER)_$(1)_$(2)$(3):
	@mkdir -p $(DIST)/stage-$(1)-$(2)
	GOOS=$(1) GOARCH=$(2) go build $(GOFLAGS) -o $(DIST)/stage-$(1)-$(2)/$(BIN)$(if $(filter windows,$(1)),.exe,) $(PKG)
	@cp README.md $(DIST)/stage-$(1)-$(2)/
	@cd $(DIST)/stage-$(1)-$(2) && $(if $(filter windows,$(1)),zip -q ../$$(@F) *,tar czf ../$$(@F) *)
	@rm -rf $(DIST)/stage-$(1)-$(2)
	@echo "built $$@"
endef

$(foreach p,$(PLATFORMS),$(eval $(call archive_rule,$(firstword $(subst /, ,$(p))),$(lastword $(subst /, ,$(p))),$(if $(filter windows,$(firstword $(subst /, ,$(p)))),.zip,.tar.gz))))

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
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

bench:
	go test ./internal/catalog/ -bench . -benchtime 50x -run XXX

clean:
	rm -rf $(DIST)
