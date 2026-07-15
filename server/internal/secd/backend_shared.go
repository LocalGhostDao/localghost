package secd

import (
	"errors"
	"path/filepath"

	"github.com/LocalGhostDao/localghost/server/internal/profile"
)

// Shared by both unlock backends (tpm and software), so it lives untagged.

var errReject = errors.New("unlock rejected")

// errHaltUnsupported: the wired backend has no maintenance stop (a test double, typically).
var errHaltUnsupported = errors.New("halt unsupported by this backend")

// loadRegistry reads the account registry from the box state dir. Written at setup/enrolment; loaded
// here for the running daemon. Absent registry is not fatal , resolution rejects every PIN until
// setup writes one.
func loadRegistry(stateDir string) (*profile.Registry, error) {
	return profile.LoadRegistry(filepath.Join(stateDir, "registry.blob"))
}
