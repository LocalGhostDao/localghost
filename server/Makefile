# LocalGhost server build. One module (github.com/LocalGhostDao/localghost/server), many binaries
# under cmd/, all sharing the library packages under internal/ (internal/auth, internal/profile,
# internal/pair, internal/hw, internal/secd, ...). internal/ means they cannot be imported outside
# this module , compiler-enforced privacy.
#
# Each cmd/<name> dir is named EXACTLY for the binary it produces (cmd/ghost.secd -> ghost.secd),
# so `go build -o bin/ ./cmd/...` and `go install ./...` produce correctly-named binaries with no
# remapping. The front door is ghost.secd; ghost-setup and ghost-ctl are the operator tools; the
# backing daemons are cmd/ghost.<x> (ghost.voiced, ghost.framed, ...). Binary names match the
# systemd unit ExecStart and the GhostDaemons list.

GO      ?= go
BINDIR  := bin
# never a toolchain from the internet: a Go older than go.mod's fails the build out loud, and
# tools/install_go.sh (run by setup.sh and redeploy.sh) brings the mirror's
export GOTOOLCHAIN ?= local
TAGS    ?=
# the version secd reports to the phone (GET /v1/update): the git description of the tree built
# here; a release sets its own tag (tools/release_build.sh)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# a tree exactly at a release's tag carries the release's name too (tools/release.names: wisp)
RELNAME ?= $(shell awk -v v="$(patsubst v%,%,$(VERSION))" '$$1 == v { print $$2; exit }' tools/release.names 2>/dev/null)
LDFLAGS := -X github.com/LocalGhostDao/localghost/server/internal/secd.Version=$(VERSION) -X github.com/LocalGhostDao/localghost/server/internal/secd.ReleaseName=$(RELNAME)

# all builds every binary under cmd/ into bin/, each named for its dir. There is ONE build now: the
# seal tier (TPM or software) is selected at RUNTIME from seal.env, not a build tag, so a box can
# change hardware without recompiling. go-tpm is always linked (pure Go, no device access at init).
.PHONY: all
all:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINDIR)/ ./cmd/...

# box is kept as an alias of all so existing muscle-memory / docs still work; there is no separate
# hardware build any more.
.PHONY: box
box: all

# Individual targets, handy during development.
.PHONY: secd
secd:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINDIR)/ ./cmd/ghost.secd

.PHONY: setup
setup:
	$(GO) build -o $(BINDIR)/ ./cmd/ghost-setup

.PHONY: ctl
ctl:
	$(GO) build -o $(BINDIR)/ ./cmd/ghost-ctl

.PHONY: test
test:
	$(GO) test ./...

.PHONY: clean
clean:
	rm -rf $(BINDIR)
