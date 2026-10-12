package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/shealth"
	"github.com/LocalGhostDao/localghost/server/internal/tally"
)

// THE SAMSUNG HEALTH DOWNLOAD, imported. Health Connect holds only what Samsung Health has
// written since it was told to share, which is weeks; the download (Samsung Health › Settings ›
// Download personal data) holds the years the watch has been worn. secd spools the zip under
// <mount>/tallyd/import; `health scan=<file>` says what is in it, `health import=<file>` reads
// it into month batches and writes them through the same ingest the phone's batches take, one
// at a time in the background, the readout following. A file the import finished is renamed
// .done so it is never read twice by accident; the zip is the person's own data and stays on
// the volume until they remove it.

type importState struct {
	mu   sync.Mutex
	prog *shealth.Progress
	busy bool
}

func (s *importState) snapshot() *shealth.Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prog == nil {
		return nil
	}
	p := *s.prog
	return &p
}

func (s *importState) set(fn func(p *shealth.Progress)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prog != nil {
		fn(s.prog)
	}
}

// importDir is where secd spools the downloads.
func importDir(mount string) string { return filepath.Join(mount, "tallyd", "import") }

// importPath keeps the file inside the import directory, whatever name the request carries.
func importPath(mount, file string) (string, error) {
	base := filepath.Base(strings.TrimSpace(file))
	if base == "" || base == "." || base == "/" || !strings.HasSuffix(strings.ToLower(base), ".zip") {
		return "", fmt.Errorf("a .zip in the import directory is wanted")
	}
	p := filepath.Join(importDir(mount), base)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%s: %v", base, err)
	}
	return p, nil
}

// start runs the import in the background; false when one is running already.
func (s *importState) start(mount, file string, lgInfo func(msg string, args ...any)) bool {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return false
	}
	s.busy = true
	s.prog = &shealth.Progress{File: filepath.Base(file), State: "reading", StartedAt: time.Now().Unix()}
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.busy = false
			s.mu.Unlock()
		}()
		fail := func(err error) {
			s.set(func(p *shealth.Progress) { p.State = "failed"; p.Error = err.Error(); p.DoneAt = time.Now().Unix() })
			lgInfo("samsung health import failed", "fn", "importState.start", "file", filepath.Base(file), "err", err)
		}
		batches, err := shealth.Batches(file, func(table string, rows int) {
			s.set(func(p *shealth.Progress) { p.Table = table; p.Tables++; p.Rows += rows })
		})
		if err != nil {
			fail(err)
			return
		}
		cfg, err := hw.LoadServicesConfig(mount)
		if err != nil {
			fail(err)
			return
		}
		db := poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port, cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
		s.set(func(p *shealth.Progress) { p.State = "writing"; p.Table = "" })
		for _, b := range batches {
			raw, _ := json.Marshal(b)
			res, err := tally.Ingest(db, raw)
			if err != nil {
				fail(err)
				return
			}
			s.set(func(p *shealth.Progress) {
				p.Months++
				p.Days += res.Days
				p.Samples += res.Samples
				if res.OldestDay != "" && (p.Oldest == "" || res.OldestDay < p.Oldest) {
					p.Oldest = res.OldestDay
				}
				if res.NewestDay > p.Newest {
					p.Newest = res.NewestDay
				}
			})
		}
		_ = os.Rename(file, file+".done")
		s.set(func(p *shealth.Progress) { p.State = "done"; p.DoneAt = time.Now().Unix() })
		lgInfo("samsung health import done", "fn", "importState.start", "file", filepath.Base(file), "months", len(batches))
	}()
	return true
}
