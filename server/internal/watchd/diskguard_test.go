package watchd

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
)

// The model's log-level directive may only make a daemon quieter: a reply of level=debug is
// refused in code, not only in the prompt (the audit's confirmed finding); a reply that raises
// the threshold is applied.
func TestDiskGuardAppliesOnlyQuieterDirectives(t *testing.T) {
	root := t.TempDir()
	if len(root) > 60 {
		// unix socket paths are short; the test's run dir must fit
		short, err := os.MkdirTemp("/tmp", "lgw")
		if err != nil {
			t.Skip("no short temp dir")
		}
		t.Cleanup(func() { os.RemoveAll(short) })
		root = short
	}
	runDir := filepath.Join(root, "run")
	logDir := filepath.Join(root, "logs")
	os.MkdirAll(logDir, 0o755)
	os.WriteFile(filepath.Join(logDir, "ghost.synthd-"+time.Now().UTC().Format("2006-01-02")+".log"), []byte("a line\n"), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	quiet := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// a stub oracled whose reply the test chooses
	reply := `{"overLogging":true,"directives":[{"service":"ghost.synthd","level":"debug"}]}`
	or := ctlsock.NewServer("ghost.oracled", runDir, quiet)
	or.Handle("infer", func(json.RawMessage) (ctlsock.Response, error) {
		b, _ := json.Marshal(oracle.Response{Output: reply})
		return ctlsock.Response{OK: true, Data: b}, nil
	})
	go or.Serve(ctx)

	// a stub ghost.synthd with the real base log-level handler over a level at INFO
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	sd := ctlsock.NewServer("ghost.synthd", runDir, quiet)
	svcconf.BindBase(sd, "ghost.synthd", lv, nil)
	go sd.Serve(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := ctlsock.NewClientTimeout("ghost.synthd", runDir, 200*time.Millisecond).Call("ping", nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stub daemon not up")
		}
		time.Sleep(20 * time.Millisecond)
	}

	g := NewDiskGuard(logDir, runDir, 0, 1024, []string{"ghost.synthd"}, quiet)
	g.Check()
	if lv.Level() != slog.LevelInfo {
		t.Fatalf("a debug directive was applied: level is %v", lv.Level())
	}
	reply = `{"overLogging":true,"directives":[{"service":"ghost.synthd","level":"WARN"}]}`
	g.Check()
	if lv.Level() != slog.LevelWarn {
		t.Fatalf("a quieter directive was not applied: level is %v", lv.Level())
	}
	reply = `{"overLogging":true,"directives":[{"service":"ghost.synthd","level":"info"}]}`
	g.Check()
	if lv.Level() != slog.LevelWarn {
		t.Fatalf("a lowering directive was applied: level is %v", lv.Level())
	}
	reply = `{"overLogging":true,"directives":[{"service":"ghost.synthd","level":"verbose"}]}`
	g.Check()
	if lv.Level() != slog.LevelWarn {
		t.Fatalf("an unknown level moved the daemon: %v", lv.Level())
	}
	if !quieter("INFO", "warn") || quieter("warn", "info") || quieter("info", "debug") || quieter("info", "info") || quieter("nonsense", "error") {
		t.Fatal("quieter()")
	}
}
