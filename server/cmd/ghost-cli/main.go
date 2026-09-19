// ghost-cli is the box's service console, the redis-cli of LocalGhost. You connect to ONE service and
// speak its command set: every service answers the base commands (ping, status, reload, log-level,
// commands), and each adds its own (ghost.oracled: infer, models; ghost.cued: queue; and so on). The
// command set you see therefore depends on which service you connect to.
//
// It dials <mount>/run/<service>.sock directly , the same control socket watchd and the daemons use ,
// so it only works on the box while UNLOCKED (the sockets live on the encrypted volume). Filesystem
// perms are the auth: you must be able to read the run-user's socket. The volume is mounted inside
// ghost.secd's mount namespace; as root, ghost-cli reaches it through /proc/<secd>/root on its own
// (internal/nsreach), so `sudo ./bin/ghost-cli ghost.framed reprocess` works straight from the repo.
//
// Usage:
//
//	ghost-cli <service> <command> [key=value ...]
//	ghost-cli ghost.oracled commands
//	ghost-cli ghost.oracled log-level level=debug
//	ghost-cli ghost.oracled reload
//	ghost-cli ghost.oracled infer capability=chat input="hello"
//	ghost-cli ghost.cued queue
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/nsreach"
)

func main() {
	args := os.Args[1:]
	// optional --mount / --run-dir before the positional args
	runDir := envOr("GHOST_RUN_DIR", "/var/lib/ghost/mnt/slot0/run")
	runDirOverridden := false
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		switch {
		case strings.HasPrefix(args[0], "--run-dir="):
			runDir = strings.TrimPrefix(args[0], "--run-dir=")
			runDirOverridden = true
		case strings.HasPrefix(args[0], "--mount="):
			runDir = strings.TrimPrefix(args[0], "--mount=") + "/run"
			runDirOverridden = true
		default:
			fatalf("unknown flag %q", args[0])
		}
		args = args[1:]
	}
	if len(args) < 2 {
		usage()
	}
	service, cmd := args[0], args[1]
	kv := parseKV(args[2:])

	// ghost.secd's socket is on the UNENCRYPTED state dir (it is the always-on process, reachable when
	// locked), unlike the cohort whose sockets are on the mounted volume. Resolve it there unless the
	// caller overrode the run dir explicitly.
	if service == "ghost.secd" && !runDirOverridden {
		runDir = envOr("GHOST_SECD_RUN_DIR", "/var/lib/ghost/run")
	}

	// THE VOLUME IS IN SECD'S MOUNT NAMESPACE. From a root shell the run dir is not there; the
	// cohort's sockets are, one door away at /proc/<secd>/root. Take that door when it is the only
	// one open , the repo's own ./bin/ghost-cli then works from anywhere, with no nsenter and no
	// copy through /tmp. Said once on stderr so the path in any later error makes sense.
	if reached := nsreach.Path(runDir); reached != runDir {
		fmt.Fprintf(os.Stderr, "(volume reached through ghost.secd's namespace: %s)\n", reached)
		runDir = reached
	} else if _, serr := os.Stat(runDir); serr != nil && service != "ghost.secd" {
		// INSTRUMENT THE SILENCE: a missing run dir with no door taken used to look identical
		// whether the box was locked, secd was not running, or this binary was simply older than
		// the door. Name what was tried, so the next line (the dial error) reads as a diagnosis.
		pids := nsreach.PIDs("ghost.secd")
		if len(pids) == 0 {
			fmt.Fprintf(os.Stderr, "(%s not visible here and ghost.secd is not running , nothing to reach)\n", runDir)
		} else {
			fmt.Fprintf(os.Stderr, "(%s not visible here; ghost.secd pid(s) %v have no %s inside their namespace either , is the box unlocked? run as root?)\n",
				runDir, pids, runDir)
		}
	}

	client := ctlsock.NewClientTimeout(service, runDir, 130*time.Second) // long enough for an inference
	resp, err := client.Call(cmd, kv)
	if err != nil {
		fatalf("%s %s: %v", service, cmd, err)
	}
	printResp(resp)
}

// parseKV turns key=value args into a map. Values are kept as strings except where they parse as an
// int, a float, or a bool, so `level=debug`, `maxTokens=256`, and `priority=1` all do the right thing
// for the handler's json.Unmarshal into typed fields.
//
// Exception: some keys are ALWAYS strings and must never be numerically coerced , a PIN like 1111 is a
// credential, not the integer 1111, and coercing it would fail the handler's `PIN string` unmarshal.
func parseKV(args []string) map[string]any {
	alwaysString := map[string]bool{"pin": true, "photo": true}
	m := map[string]any{}
	for _, a := range args {
		i := strings.IndexByte(a, '=')
		if i < 0 {
			m[a] = true // bare flag
			continue
		}
		k, v := a[:i], a[i+1:]
		if alwaysString[k] {
			m[k] = v
			continue
		}
		if n, err := strconv.Atoi(v); err == nil {
			m[k] = n
		} else if f, err := strconv.ParseFloat(v, 64); err == nil {
			m[k] = f
		} else if b, err := strconv.ParseBool(v); err == nil {
			m[k] = b
		} else {
			m[k] = v
		}
	}
	return m
}

func printResp(resp ctlsock.Response) {
	if resp.Text != "" {
		fmt.Println(resp.Text)
	}
	if len(resp.Data) > 0 {
		// pretty-print the data payload
		var pretty any
		if err := json.Unmarshal(resp.Data, &pretty); err == nil {
			b, _ := json.MarshalIndent(pretty, "", "  ")
			fmt.Println(string(b))
		} else {
			fmt.Println(string(resp.Data))
		}
	}
	if resp.Text == "" && len(resp.Data) == 0 {
		fmt.Println("ok")
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ghost-cli [--run-dir=DIR|--mount=DIR] <service> <command> [key=value ...]")
	fmt.Fprintln(os.Stderr, "  every service: ping | status | reload | log-level [level=..] | commands")
	fmt.Fprintln(os.Stderr, "  ghost.oracled: infer capability=.. input=.. | models")
	fmt.Fprintln(os.Stderr, "  discover a service's commands: ghost-cli <service> commands")
	os.Exit(2)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
