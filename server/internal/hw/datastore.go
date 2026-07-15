package hw

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/searchsql"
)

// DataStore manages the per-account Postgres + Redis instances. Each account's databases live INSIDE
// that account's encrypted container (so the data is encrypted at rest with the account key and
// vanishes on crypto-erase), and run only while the account is mounted. ghost.secd starts them on
// unlock and stops them on lock, and routes queries to the mounted account's endpoints.
//
// This is the seam the unlock flow's StartDB / StartCache stages drive. Distinct ports per slot so
// instances never collide; bound to loopback only (the account daemons reach them, nothing external).
//
// NOT validated in CI (needs postgres + redis binaries + mounted volumes). Built against pg_ctl /
// initdb / redis-server; exercise on the box.

type Endpoints struct {
	PostgresPort int
	RedisPort    int
	Socket       string // postgres unix socket dir (loopback alternative)
}

type DataStore struct {
	// mountPathFor returns where a slot's container is mounted (from the Mounter).
	mountPathFor func(slot int) string
	// runUser is the unprivileged account Postgres and Redis must run as. secd runs as root (it mounts
	// dm-crypt), but Postgres REFUSES to run as root ("initdb: cannot be run as root"), so every DB
	// process is dropped to this user. Empty means no drop , only valid in tests that never spawn a DB.
	runUser string
}

func NewDataStore(mountPathFor func(slot int) string, runUser string) *DataStore {
	return &DataStore{mountPathFor: mountPathFor, runUser: runUser}
}

// dbCredential returns the syscall.Credential to drop DB processes to runUser, or nil if unset/unknown.
func (d *DataStore) dbCredential() *syscall.Credential {
	if d.runUser == "" {
		return nil
	}
	u, err := user.Lookup(d.runUser)
	if err != nil {
		return nil
	}
	uid, e1 := strconv.Atoi(u.Uid)
	gid, e2 := strconv.Atoi(u.Gid)
	if e1 != nil || e2 != nil {
		return nil
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
}

// cfg reads services.conf from the slot's mounted volume. Ports and passwords are the file's, not
// derived , the file is the single source of truth (see services_config.go). An unreadable config on
// a mounted volume is a real error surfaced to the caller, not a silent default.
func (d *DataStore) cfg(slot int) (ServicesConfig, error) {
	return LoadServicesConfig(d.mountPathFor(slot))
}

func (d *DataStore) pgPortCfg(slot int) (int, error) {
	c, err := d.cfg(slot)
	if err != nil {
		return 0, err
	}
	return c.Postgres.Port, nil
}
func (d *DataStore) redisPortCfg(slot int) (int, error) {
	c, err := d.cfg(slot)
	if err != nil {
		return 0, err
	}
	return c.Redis.Port, nil
}

// pgPort / redisPort are the DEFAULT loopback ports, used by the notification + mute stores which
// connect to the already-running databases. They match ServicesConfig's defaults (6000/6100). NOTE
// (honest limit): if a box overrides these in services.conf, these two stores would still use the
// defaults , they do not read the config, because they run in hot paths without the mount handle.
// Today provision always writes the defaults, so they agree. If per-box port override becomes real,
// these stores must be threaded with the config port like DataStore was. Tracked, not hidden.
func pgPort(slot int) int    { return 6000 + slot }
func redisPort(slot int) int { return 6100 + slot }

func (d *DataStore) pgData(slot int) string {
	return filepath.Join(d.mountPathFor(slot), "postgres")
}
func (d *DataStore) redisDir(slot int) string {
	return filepath.Join(d.mountPathFor(slot), "redis")
}

// Start brings up the account's Postgres and Redis (initialising the cluster on first run), and
// returns the endpoints for ghost.secd to route to. Called during the unlock StartDB/StartCache
// stages, AFTER the container is mounted (so the data dirs are inside the decrypted volume).
func (d *DataStore) Start(slot int) (Endpoints, error) {
	c, err := d.cfg(slot)
	if err != nil {
		return Endpoints{}, err
	}
	if err := d.startPostgres(slot, c); err != nil {
		return Endpoints{}, err
	}
	if err := d.startRedis(slot, c); err != nil {
		_ = d.stopPostgres(slot, c)
		return Endpoints{}, err
	}
	return Endpoints{
		PostgresPort: c.Postgres.Port,
		RedisPort:    c.Redis.Port,
		Socket:       d.pgData(slot),
	}, nil
}

// Stop tears both down on lock/unmount, so nothing holds the volume open when we close it.
func (d *DataStore) Stop(slot int) error {
	c, err := d.cfg(slot)
	if err != nil {
		return err
	}
	rerr := d.stopRedis(slot, c)
	perr := d.stopPostgres(slot, c)
	if perr != nil {
		return perr
	}
	return rerr
}

// StopCache stops this slot's Redis. Split out so the lock teardown can report it as its own step.
func (d *DataStore) StopCache(slot int) error {
	c, err := d.cfg(slot)
	if err != nil {
		return err
	}
	return d.stopRedis(slot, c)
}

// StopDB stops this slot's Postgres. Split out so the lock teardown can report it as its own step.
func (d *DataStore) StopDB(slot int) error {
	c, err := d.cfg(slot)
	if err != nil {
		return err
	}
	return d.stopPostgres(slot, c)
}

// pgAlive reports whether this slot's Postgres is already up. pg_ctl status reads the postmaster.pid
// in the data dir and probes the process: exit 0 means a live server. This is the convergence probe
// that lets startPostgres run at EVERY unlock , alive means skip the start, converge the schema only.
func (d *DataStore) pgAlive(slot int) bool {
	data := d.pgData(slot)
	return d.pgCmd(filepath.Dir(data), "pg_ctl", "-D", data, "status").Run() == nil
}

func (d *DataStore) startPostgres(slot int, c ServicesConfig) error {
	data := d.pgData(slot)
	// CONVERGE, not start: unlock now runs this every time, including when the volume stayed mounted
	// across a secd restart (the mounted-but-dead state that used to be unrepairable , Warm meant
	// "mounted", unlock skipped this stage, and a dead Postgres on a live mount stayed dead forever).
	// A live server needs no start; it still gets EnsureSchema, the every-unlock rule.
	if d.pgAlive(slot) {
		return d.EnsureSchema(slot, c)
	}
	firstRun := false
	// initdb on first run (the data dir lives in the encrypted volume).
	if _, err := os.Stat(filepath.Join(data, "PG_VERSION")); os.IsNotExist(err) {
		firstRun = true
		if err := os.MkdirAll(data, 0o700); err != nil {
			return err
		}
		// secd runs as root, so MkdirAll just made this dir ROOT-owned , but initdb runs dropped to the
		// service user (Postgres refuses root). initdb must be able to TRAVERSE from the mount root down
		// to, and WRITE in, the data dir. So: make the mounted volume root traversable by the run user,
		// and chown the postgres data dir (and its parent) to them, BEFORE initdb. Without the traversal
		// chain, initdb-as-coder gets "could not access directory: Permission denied" even owning the
		// leaf dir , which is exactly the failure this fixes.
		mountRoot := d.mountPathFor(slot)
		if cred := d.dbCredential(); cred != nil {
			// These are load-bearing: if any fails, initdb (as the run user) WILL fail , so failing
			// here with the real reason beats initdb failing later with a vaguer one.
			for _, dir := range []string{data, filepath.Dir(data), mountRoot} {
				if err := os.Chown(dir, int(cred.Uid), int(cred.Gid)); err != nil {
					return fmt.Errorf("chown %s to run user before initdb: %w", dir, err)
				}
			}
		}
		// The volume root is 0700 from mkfs; 0711 lets the run user path through to its data dirs.
		if err := os.Chmod(mountRoot, 0o711); err != nil {
			return fmt.Errorf("chmod volume root %s traversable: %w", mountRoot, err)
		}
		if out, err := d.pgCmd(filepath.Dir(data), "initdb", "-D", data, "--auth=trust", "--encoding=UTF8").CombinedOutput(); err != nil {
			return fmt.Errorf("initdb slot %d: %v: %s", slot, err, strings.TrimSpace(string(out)))
		}
	}
	// start, bound to loopback on the config's port, socket inside the volume.
	// -l logfile: capture Postgres's OWN startup output. Without it, a server that fails to come up
	// leaves pg_ctl -w polling to timeout with no reason recorded anywhere , the "hangs at starting
	// database" black box. With it, the failure reason lands in the volume where we can read it.
	startLog := filepath.Join(data, "startup.log")
	opts := fmt.Sprintf("-p %d -k %s -c listen_addresses=127.0.0.1", c.Postgres.Port, data)
	cmd := d.pgCmd(filepath.Dir(data), "pg_ctl", "-D", data, "-l", startLog, "-o", opts, "-w", "-t", "30", "start")
	if out, err := cmd.CombinedOutput(); err != nil {
		// Fold Postgres's own log into the error so the journal shows WHY it would not start.
		pglog := ""
		if b, rerr := os.ReadFile(startLog); rerr == nil {
			pglog = strings.TrimSpace(string(b))
		}
		return fmt.Errorf("pg_ctl start slot %d: %v: %s | postgres log: %s", slot, err, strings.TrimSpace(string(out)), pglog)
	}
	// On first run only: apply the provisioned password to the ghost role and lay down the app config
	// schema. Runs AFTER start (needs a live server) and is idempotent-guarded by firstRun.
	if firstRun {
		if err := d.initPostgresAuthAndSchema(slot, c); err != nil {
			_ = d.stopPostgres(slot, c)
			return fmt.Errorf("init db auth/schema slot %d: %w", slot, err)
		}
	}
	// EVERY unlock, not just the first: converge the live database to the current code , tables,
	// roles, grants, search layer. Idempotent by rule; drift-proof by construction.
	if err := d.EnsureSchema(slot, c); err != nil {
		_ = d.stopPostgres(slot, c)
		return fmt.Errorf("ensure schema slot %d: %w", slot, err)
	}
	return nil
}

// appConfigSchema is the application schema , every statement idempotent (IF NOT EXISTS) BY RULE,
// because it now runs at EVERY unlock via EnsureSchema, not just at provision. A new table added
// here reaches live volumes on their next unlock; before this rule, weeks-old volumes silently
// lacked every table added since their provision day (sync_cursors, chats, frame_tags , the lot).
const appConfigSchema = `
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
-- notification mute, per scope. scope '*' is the global mute (overrides everything); scope
-- 'ghost.synthd' etc. is a per-service mute. muted_until: a timestamp the mute is active until;
-- a far-future value means "forever". A row's absence (or muted_until in the past) = not muted.
CREATE TABLE IF NOT EXISTS notification_mute (
  scope       TEXT PRIMARY KEY,
  muted_until TIMESTAMPTZ NOT NULL
);
-- notifications: always produced by the daemons (mute only affects push, not storage). Durable
-- history with a seen flag; deletable forever. The Redis last-100 list is the hot push cache.
CREATE TABLE IF NOT EXISTS notifications (
  id       BIGSERIAL PRIMARY KEY,
  service  TEXT NOT NULL,
  kind     TEXT NOT NULL DEFAULT 'message',
  title    TEXT NOT NULL DEFAULT '',
  body     TEXT NOT NULL DEFAULT '',
  seen     BOOLEAN NOT NULL DEFAULT FALSE,
  -- An "ask" is a notification the user can answer (ghost.cued nominations, confirmations). options
  -- is a JSON array of choices; empty means this is a passive notification (telling, not asking).
  -- answer is the chosen option once picked; answered is when. A pending ask has answer='' answered NULL.
  options  TEXT NOT NULL DEFAULT '',
  answer   TEXT NOT NULL DEFAULT '',
  answered TIMESTAMPTZ,
  created  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS notifications_id_desc ON notifications (id DESC);
-- frames: ghost.framed's photo archive index. hash is the identity (sha256/128 of the raw bytes),
-- archive_path holds the untouched original, preview/thumb are derived JPEGs. taken_at from EXIF
-- DateTimeOriginal, falling back to upload mtime. Re-inserts are ON CONFLICT DO NOTHING , idempotent.
CREATE TABLE IF NOT EXISTS frames (
  hash         TEXT PRIMARY KEY,
  taken_at     BIGINT NOT NULL DEFAULT 0,
  lat          DOUBLE PRECISION NOT NULL DEFAULT 0,
  lon          DOUBLE PRECISION NOT NULL DEFAULT 0,
  has_gps      BOOLEAN NOT NULL DEFAULT FALSE,
  archive_path TEXT NOT NULL,
  preview_path TEXT NOT NULL DEFAULT '',
  thumb_path   TEXT NOT NULL DEFAULT '',
  bytes        BIGINT NOT NULL DEFAULT 0,
  source       TEXT NOT NULL DEFAULT '',
  received_at  BIGINT NOT NULL DEFAULT 0,
  kind         TEXT NOT NULL DEFAULT 'unknown',
  mime         TEXT NOT NULL DEFAULT ''
);
-- Older databases created before kind/mime existed: add the columns if missing (idempotent).
ALTER TABLE frames ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE frames ADD COLUMN IF NOT EXISTS mime TEXT NOT NULL DEFAULT '';
-- taken_src: how taken_at was determined. Existing rows default to 'mtime' , the HONEST default,
-- because pre-column rows fell back to upload mtime when EXIF was absent, and the where-was-I sync
-- query must not trust those (one poisons MAX(taken_at) and the phone skips its whole roll).
ALTER TABLE frames ADD COLUMN IF NOT EXISTS taken_src TEXT NOT NULL DEFAULT 'mtime';

-- Per-device sync cursors. The phone's local cursor dies with every app reinstall; the box remembers
-- for it. where-was-I answers max(trusted content time, stored cursor) , so a fresh install resumes
-- instead of re-walking the whole roll. Keyed by device for the multi-device future; today "default".
CREATE TABLE IF NOT EXISTS sync_cursors (
    device TEXT NOT NULL,
    kind   TEXT NOT NULL,
    ts     BIGINT NOT NULL DEFAULT 0,
    id     BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (device, kind)
);

-- Chat persistence. Incognito conversations simply never reach these tables.
CREATE TABLE IF NOT EXISTS chats (
    id         BIGSERIAL PRIMARY KEY,
    title      TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS chat_messages (
    id      BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    role    TEXT NOT NULL,
    content TEXT NOT NULL,
    ts      BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS chat_messages_chat ON chat_messages (chat_id, id);

-- Human-facing frame metadata. display_name is DERIVED (date + first tags) by the tag worker and
-- only ever set when empty , a user rename wins forever. Archive files stay hash-named: bytes are
-- the identity, names are decoration.
ALTER TABLE frames ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS frame_tags (
    hash       TEXT NOT NULL,
    tag        TEXT NOT NULL,
    source     TEXT NOT NULL DEFAULT 'model', -- 'model' | 'user' | 'user_removed' (tombstone: model may not resurrect)
    created_at BIGINT NOT NULL,
    PRIMARY KEY (hash, tag)
);
CREATE INDEX IF NOT EXISTS frame_tags_tag ON frame_tags (tag);
CREATE INDEX IF NOT EXISTS frames_taken_at ON frames (taken_at);
CREATE INDEX IF NOT EXISTS frames_kind ON frames (kind);
-- location_points: watch/phone position samples, the raw material for the daily GeoJSON path. The
-- box stores coordinates and NEVER contacts a map/tile service; rendering is the phone's job.
CREATE TABLE IF NOT EXISTS location_points (
  ts     BIGINT NOT NULL,
  lat    DOUBLE PRECISION NOT NULL,
  lon    DOUBLE PRECISION NOT NULL,
  source TEXT NOT NULL DEFAULT 'watch',
  PRIMARY KEY (ts, source)
);
`

// initPostgresAuthAndSchema applies the PROVISIONED password (from services.conf) to the ghost role
// and creates the app config schema. Called once at first start, while the volume is mounted. The
// password is generated at provision, not here, so services.conf remains the single source of truth.
func (d *DataStore) initPostgresAuthAndSchema(slot int, c ServicesConfig) error {
	data := d.pgData(slot)
	port := fmt.Sprint(c.Postgres.Port)

	// The password is PROVISIONED in services.conf (generated at setup), not made up here , that file
	// is the single credential store, and it must match what gates TCP. Apply it to the ghost role
	// over the trust-auth loopback socket.
	if err := pgIdent(c.Postgres.User); err != nil {
		return fmt.Errorf("services.conf postgres user: %w", err)
	}
	if err := pgIdent(c.Postgres.Name); err != nil {
		return fmt.Errorf("services.conf postgres db name: %w", err)
	}
	stmts := []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD %s;", c.Postgres.User, pgLit(c.Postgres.Password)),
		fmt.Sprintf("CREATE DATABASE %s OWNER %s;", c.Postgres.Name, c.Postgres.User),
	}
	for _, s := range stmts {
		if out, err := d.pgCmd(filepath.Dir(data), "psql", "-h", data, "-p", port, "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c", s).CombinedOutput(); err != nil {
			return fmt.Errorf("psql %q: %v: %s", s, err, strings.TrimSpace(string(out)))
		}
	}

	// app config schema, in the ghost database. Tables: settings (k/v), and the notification mute.
	schema := appConfigSchema
	if out, err := d.pgCmd(filepath.Dir(data), "psql", "-h", data, "-p", port, "-d", c.Postgres.Name, "-v", "ON_ERROR_STOP=1", "-c", schema).CombinedOutput(); err != nil {
		return fmt.Errorf("apply schema: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// Role split. ghost_ro can only SELECT; ghost_rw can write the app tables. Daemons connect as one
	// of these, never as the owner. Grants cover existing tables plus a DEFAULT PRIVILEGES rule so
	// tables added by later migrations inherit the same access without re-granting. ghost_rw also needs
	// USAGE on sequences (BIGSERIAL id) to INSERT.
	for _, ident := range []string{c.Postgres.ROUser, c.Postgres.RWUser} {
		if err := pgIdent(ident); err != nil {
			return fmt.Errorf("services.conf service role: %w", err)
		}
	}
	roleSQL := fmt.Sprintf(`
CREATE ROLE %[1]s LOGIN PASSWORD %[2]s;
CREATE ROLE %[3]s LOGIN PASSWORD %[4]s;
GRANT CONNECT ON DATABASE %[5]s TO %[1]s, %[3]s;
GRANT USAGE ON SCHEMA public TO %[1]s, %[3]s;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO %[1]s;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %[3]s;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %[3]s;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO %[1]s;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %[3]s;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO %[3]s;`,
		c.Postgres.ROUser, pgLit(c.Postgres.ROPass), c.Postgres.RWUser, pgLit(c.Postgres.RWPass), c.Postgres.Name)
	if out, err := d.pgCmd(filepath.Dir(data), "psql", "-h", data, "-p", port, "-d", c.Postgres.Name, "-v", "ON_ERROR_STOP=1", "-c", roleSQL).CombinedOutput(); err != nil {
		return fmt.Errorf("create roles: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// Search layer schema (SPEC v1.1), applied as the OWNER over the still-trust socket (the
	// migrations-run-as-owner rule). Order matters: this must run BEFORE pg_hba hardening, because the
	// owner role authenticates by trust only. pgvector is optional , absence is the documented FTS-only
	// degraded mode, recorded in search.meta so ghost.searchd reports it honestly.
	if err := d.applySearchSchema(data, port, c); err != nil {
		return fmt.Errorf("apply search schema: %w", err)
	}

	// Harden pg_hba: switch local socket auth from trust to scram-sha-256, so a role's password is
	// actually verified (the whole point of the split , without this, ghost_ro could log in as
	// ghost_rw). initdb wrote passwords as scram already (default password_encryption). Reload for the
	// new pg_hba to take effect; the owner connection above used trust, which still worked pre-reload.
	if err := d.hardenPgHBA(slot, c); err != nil {
		return fmt.Errorf("harden pg_hba: %w", err)
	}

	// No separate credential file , services.conf (written at provision) is the single credential
	// store, read by DataStore and anything else that needs to connect. The password was applied to
	// the role above; ghost.secd reads it from services.conf to connect over TCP.
	return nil
}

// hardenPgHBA rewrites pg_hba.conf so local and loopback connections use scram-sha-256 instead of the
// initdb trust default, then reloads Postgres. After this, a password is actually checked , which is
// what makes the ghost_ro / ghost_rw split real: ghost_ro cannot present ghost_rw's password it does
// not have. The socket is still loopback/volume-local; scram is defence in depth on top of that, not
// a replacement for it.
//
// The run user keeps PEER auth on the socket , first match wins, so its line comes first. That user
// is the initdb superuser, and it is the BOOTSTRAP identity EnsureSchema uses to create the owner
// role and database on volumes that predate them. Without this line, scram-everything locks the
// superuser out entirely (it has no password), and a box whose owner role is missing or broken has
// no identity left that can repair it. Peer is not a hole: it authenticates by OS uid over the
// volume-local socket, the same trust the whole run-user model already stands on.
//
// Idempotent (same bytes, cheap reload), so EnsureSchema converges it at every unlock , old volumes
// hardened before this line existed pick it up on their next unlock.
func (d *DataStore) hardenPgHBA(slot int, c ServicesConfig) error {
	data := d.pgData(slot)
	peer := ""
	if d.runUser != "" && !strings.ContainsAny(d.runUser, " \t\n\"") {
		peer = fmt.Sprintf("local   all   %s   peer\n", d.runUser)
	}
	hba := "# ghost: scram-sha-256 on the volume-local socket and loopback; peer for the run user\n" +
		"# (the initdb superuser , the bootstrap identity). Converged at every unlock.\n" +
		peer +
		"local   all   all                  scram-sha-256\n" +
		"host    all   all   127.0.0.1/32   scram-sha-256\n" +
		"host    all   all   ::1/128        scram-sha-256\n"
	if err := os.WriteFile(filepath.Join(data, "pg_hba.conf"), []byte(hba), 0o600); err != nil {
		return err
	}
	// The file must be readable by the server process (the run user); secd writes as root.
	if cred := d.dbCredential(); cred != nil {
		_ = os.Chown(filepath.Join(data, "pg_hba.conf"), int(cred.Uid), int(cred.Gid))
	}
	out, err := d.pgCmd(filepath.Dir(data), "pg_ctl", "-D", data, "reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("pg_ctl reload: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// --- volume-resident DB runtime ---
// The encrypted drive can carry the database BINARIES too, not just the data. tools/
// bundle_db_runtime.sh mirrors Debian's Postgres tree (usr/lib/postgresql/<ver> + usr/share/
// postgresql/<ver> , the structure must travel intact because PG relocates by RELATIVE offset from
// where the binary actually is) plus the redis binaries and both shared-library closures into
// <mount>/runtime. When that runtime exists, every DB process this file spawns comes from the volume:
// binaries live and die with the mount, version-pinned to the data they initdb'd, immune to OS apt
// upgrades. When it does not exist (first bring-up), PATH , the OS packages , is the fallback, and
// after bundling the OS packages can be purged entirely.

// pgRuntimeBin locates the bundled Postgres bin dir and the LD_LIBRARY_PATH to run it with.
func pgRuntimeBin(mount string) (string, string, bool) {
	globs, _ := filepath.Glob(filepath.Join(mount, "runtime", "pgroot", "usr", "lib", "postgresql", "*", "bin"))
	if len(globs) == 0 {
		return "", "", false
	}
	sort.Strings(globs)
	bin := globs[len(globs)-1]
	ld := filepath.Join(filepath.Dir(bin), "lib") + ":" + filepath.Join(mount, "runtime", "pgroot", "lib")
	return bin, ld, true
}

// runtimeUsable reports whether the volume bundle can actually be EXECUTED by the run user. A bundle
// built by a sudo run without a chown is root-owned, and every parent dir up to the mount may be
// root-only , so fork/exec as the dropped-to service user fails "permission denied". We check that the
// binary is owned by the run user (or world-executable with a traversable path). If not, callers fall
// back to OS packages, which are always world-executable. This is the guard that stops a broken
// bundle from wedging unlock; the bundle script's own chown is the fix, this is the safety net.
func (d *DataStore) runtimeUsable(bin string) bool {
	cred := d.dbCredential()
	if cred == nil {
		return true // no privilege drop (tests): whatever secd can run is fine
	}
	fi, err := os.Stat(filepath.Join(bin, "initdb"))
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	// Owned by the run user with an exec bit, OR world-executable. Ownership is the reliable signal for
	// a correctly-bundled tree (the script chowns to the run user).
	ownedAndExec := st.Uid == cred.Uid && fi.Mode().Perm()&0o100 != 0
	worldExec := fi.Mode().Perm()&0o001 != 0
	return ownedAndExec || worldExec
}

// osPgBin resolves the OS-package Postgres bin dir (e.g. /usr/lib/postgresql/18/bin), which is NOT on
// the default PATH on Debian , so a bare exec.Command("initdb") fails "not found in $PATH". Highest
// version wins, matching how the setup scripts pick it. Empty if none installed.
func osPgBin() string {
	globs, _ := filepath.Glob("/usr/lib/postgresql/*/bin")
	if len(globs) == 0 {
		return ""
	}
	sort.Strings(globs)
	return globs[len(globs)-1]
}

// pgCmd builds a command for a Postgres binary, volume runtime first, OS package fallback.
func (d *DataStore) pgCmd(mount, name string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if bin, ld, ok := pgRuntimeBin(mount); ok && d.runtimeUsable(bin) {
		cmd = exec.Command(filepath.Join(bin, name), args...)
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+ld)
	} else if osbin := osPgBin(); osbin != "" {
		// OS package fallback , resolve the real bin dir; initdb/pg_ctl are not on PATH on Debian.
		cmd = exec.Command(filepath.Join(osbin, name), args...)
	} else {
		cmd = exec.Command(name, args...) // last resort: hope it is on PATH
	}
	// Drop to the unprivileged user , Postgres will not run as root. This is the fix for unlock's
	// DB-start failing and rolling back the whole mount.
	if cred := d.dbCredential(); cred != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	}
	return cmd
}

// redisCmd is the same contract for redis-server / redis-cli.
func (d *DataStore) redisCmd(mount, name string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	bin := filepath.Join(mount, "runtime", "redis", "bin", name)
	if fi, err := os.Stat(bin); err == nil && fi.Mode().Perm()&0o111 != 0 {
		cmd = exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+filepath.Join(mount, "runtime", "redis", "lib"))
	} else if p, err := exec.LookPath(name); err == nil {
		cmd = exec.Command(p, args...) // OS package, absolute path (redis is usually /usr/bin, on PATH)
	} else {
		cmd = exec.Command(name, args...)
	}
	if cred := d.dbCredential(); cred != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	}
	return cmd
}

// pgIdent admits only strict lower-case identifiers for role/database names sourced from
// services.conf. services.conf is DATA , generated by us, but readable and theoretically editable on
// disk , and these names are spliced into owner-privilege SQL where quoting rules differ from string
// literals. A name outside [a-z_][a-z0-9_]* means the conf was tampered with or corrupted; refusing
// loudly beats accepting quietly.
func pgIdent(name string) error {
	if name == "" {
		return fmt.Errorf("empty identifier")
	}
	for i, c := range name {
		ok := c == '_' || (c >= 'a' && c <= 'z') || (i > 0 && c >= '0' && c <= '9')
		if !ok {
			return fmt.Errorf("identifier %q outside [a-z_][a-z0-9_]*", name)
		}
	}
	return nil
}

// pgLit renders a string as a single-quoted SQL literal with quotes doubled. The passwords this wraps
// are randHex by construction, so the escape is normally a no-op , it exists so a hand-edited
// services.conf cannot turn provisioning into superuser SQL injection.
func pgLit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// ensureOwnerAndDB is EnsureSchema's BOOTSTRAP phase, run as the SUPERUSER (the run user initdb
// created) over the volume-local socket with peer auth. It exists because the rest of EnsureSchema
// authenticates as the owner role , which is circular on any volume where that role is missing or
// broken: the converger could not log in as the role it was supposed to create (observed live:
// FATAL "role ghost does not exist", every unlock, forever). This phase converges the things only a
// superuser can: the owner role exists with the provisioned password, the database exists, and
// every app object in it , database, schema public, tables, sequences, views , is OWNED by the owner
// role, so the owner-authed phase that follows can always apply schema and grants. Ownership matters
// on pre-role volumes: their objects belong to the superuser, and an owner role that owns nothing
// cannot CREATE in public (PG15+) or GRANT on tables it does not own. Everything here is idempotent.
func (d *DataStore) ensureOwnerAndDB(slot int, c ServicesConfig) error {
	data := d.pgData(slot)
	port := strconv.Itoa(c.Postgres.Port)
	if err := pgIdent(c.Postgres.User); err != nil {
		return fmt.Errorf("services.conf postgres user: %w", err)
	}
	if err := pgIdent(c.Postgres.Name); err != nil {
		return fmt.Errorf("services.conf postgres db name: %w", err)
	}
	// No -U: psql defaults to the OS user, and pgCmd drops the process to the run user , which is the
	// initdb superuser and matches the peer line in pg_hba. That identity needs no password, which is
	// the whole point of a bootstrap identity.
	super := func(db, sql string) (string, error) {
		out, err := d.pgCmd(filepath.Dir(data), "psql", "-h", data, "-p", port, "-d", db, "-tA",
			"-v", "ON_ERROR_STOP=1", "-c", sql).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	owner := c.Postgres.User
	// Roles are a SUPERUSER concern, all of them: the owner, ghost_ro, ghost_rw. Creating a role or
	// setting its password needs CREATEROLE, which the owner deliberately does not have , the
	// owner-authed phase attempting CREATE ROLE was the second circle this bootstrap breaks (observed
	// live: "permission denied to create role" on a volume that never had the service roles).
	// Create-if-missing, then ALTER unconditionally so LOGIN and the password always match
	// services.conf (the single credential truth).
	roleDefs := []struct{ name, pass string }{
		{owner, c.Postgres.Password},
	}
	if c.Postgres.ROUser != "" && c.Postgres.RWUser != "" {
		for _, ident := range []string{c.Postgres.ROUser, c.Postgres.RWUser} {
			if err := pgIdent(ident); err != nil {
				return fmt.Errorf("services.conf service role: %w", err)
			}
		}
		roleDefs = append(roleDefs,
			struct{ name, pass string }{c.Postgres.ROUser, c.Postgres.ROPass},
			struct{ name, pass string }{c.Postgres.RWUser, c.Postgres.RWPass},
		)
	}
	for _, rd := range roleDefs {
		roleSQL := fmt.Sprintf(`
DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = '%[1]s') THEN CREATE ROLE %[1]s; END IF;
END $$;
ALTER ROLE %[1]s LOGIN PASSWORD %[2]s;`, rd.name, pgLit(rd.pass))
		if _, err := super("postgres", roleSQL); err != nil {
			return fmt.Errorf("ensure role %s: %w", rd.name, err)
		}
	}
	// Database: CREATE DATABASE cannot run inside a DO block, so existence is checked first.
	exists, err := super("postgres", fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = %s", pgLit(c.Postgres.Name)))
	if err != nil {
		return fmt.Errorf("check database: %w", err)
	}
	if exists != "1" {
		if _, err := super("postgres", fmt.Sprintf("CREATE DATABASE %s OWNER %s;", c.Postgres.Name, owner)); err != nil {
			return fmt.Errorf("create database: %w", err)
		}
	}
	// Ownership converge inside the database. ALTER ... OWNER TO the current owner is a no-op, so on
	// a healthy box this whole block costs one round trip and changes nothing.
	ownSQL := fmt.Sprintf(`
ALTER DATABASE %[1]s OWNER TO %[2]s;
ALTER SCHEMA public OWNER TO %[2]s;
DO $$ DECLARE r record; BEGIN
  FOR r IN SELECT tablename AS n FROM pg_tables WHERE schemaname = 'public' LOOP
    EXECUTE format('ALTER TABLE public.%%I OWNER TO %[2]s', r.n);
  END LOOP;
  FOR r IN SELECT sequencename AS n FROM pg_sequences WHERE schemaname = 'public' LOOP
    EXECUTE format('ALTER SEQUENCE public.%%I OWNER TO %[2]s', r.n);
  END LOOP;
  FOR r IN SELECT viewname AS n FROM pg_views WHERE schemaname = 'public' LOOP
    EXECUTE format('ALTER VIEW public.%%I OWNER TO %[2]s', r.n);
  END LOOP;
END $$;`, c.Postgres.Name, owner)
	if _, err := super(c.Postgres.Name, ownSQL); err != nil {
		return fmt.Errorf("converge ownership: %w", err)
	}
	return nil
}

// EnsureSchema converges a LIVE database to the current code: application tables, service roles,
// grants, and the search layer , all idempotent, all at every unlock. This exists because the
// original design ran schema only at provision (firstRun), so every volume older than a code
// change was missing whatever that change added. Symptom set on a real box: ghost_rw "does not
// exist", cursors resetting to zero, chats never persisting , one drift, many faces. Auth: the
// owner role over scram (pg_hba is hardened by now), password from services.conf.
func (d *DataStore) EnsureSchema(slot int, c ServicesConfig) error {
	data := d.pgData(slot)
	port := strconv.Itoa(c.Postgres.Port)
	// PHASE 0, as the superuser: converge pg_hba FIRST (so the run user's peer line exists , without
	// it, a scram-everything hba from an earlier hardening locks the bootstrap identity out), then
	// bootstrap the owner role, database, and object ownership. Only after this can the owner-authed
	// phase below be relied on , the observed failure mode was this exact circle: EnsureSchema
	// authenticating as an owner role that its own later steps were supposed to create.
	if err := d.hardenPgHBA(slot, c); err != nil {
		return fmt.Errorf("converge pg_hba: %w", err)
	}
	if err := d.ensureOwnerAndDB(slot, c); err != nil {
		return fmt.Errorf("bootstrap owner/db: %w", err)
	}
	run := func(sql string) error {
		cmd := d.pgCmd(filepath.Dir(data), "psql", "-h", data, "-p", port,
			"-U", c.Postgres.User, "-d", c.Postgres.Name, "-v", "ON_ERROR_STOP=1", "-c", sql)
		if cmd.Env == nil {
			cmd.Env = os.Environ() // pgCmd's OS-fallback branch leaves Env nil = inherit; keep that
		}
		cmd.Env = append(cmd.Env, "PGPASSWORD="+c.Postgres.Password)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run(appConfigSchema); err != nil {
		return fmt.Errorf("ensure app schema: %w", err)
	}
	// Service-role GRANTS , the owner may grant on what it owns (everything, post-bootstrap), and
	// ONLY grant: role creation and passwords are CREATEROLE work, done in ensureOwnerAndDB as the
	// superuser. The roles are guaranteed to exist by the time this runs.
	for _, ident := range []string{c.Postgres.ROUser, c.Postgres.RWUser} {
		if err := pgIdent(ident); err != nil {
			return fmt.Errorf("service role ident: %w", err)
		}
	}
	roleEnsure := fmt.Sprintf(`
GRANT CONNECT ON DATABASE %[3]s TO %[1]s, %[2]s;
GRANT USAGE ON SCHEMA public TO %[1]s, %[2]s;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO %[1]s;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %[2]s;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %[2]s;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO %[1]s;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %[2]s;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO %[2]s;`,
		c.Postgres.ROUser, c.Postgres.RWUser, c.Postgres.Name)
	if err := run(roleEnsure); err != nil {
		return fmt.Errorf("ensure grants: %w", err)
	}
	// Search layer, same converge rule: core must apply; vector is best-effort with the documented
	// FTS-only fallback , mirrors provision, authed for the hardened world.
	if err := run(searchsql.SchemaCore); err != nil {
		return fmt.Errorf("ensure search core: %w", err)
	}
	if err := run(searchsql.SchemaVector); err != nil {
		if e2 := run(searchsql.SchemaNoVector + searchsql.HealthViewNoVector); e2 != nil {
			return fmt.Errorf("ensure search no-vector fallback: %w", e2)
		}
	} else if err := run(searchsql.HealthView); err != nil {
		return fmt.Errorf("ensure search health view: %w", err)
	}
	// Search-schema GRANTS , the piece the provision path runs last and this converge previously
	// never ran at all: the public-schema grants above do not reach schema search, so the service
	// roles could see the search tables exist and touch none of them (observed live: ghost_rw,
	// "permission denied for schema search", one warn per ingest, forever).
	if err := run(searchsql.Grants); err != nil {
		return fmt.Errorf("ensure search grants: %w", err)
	}
	return nil
}

// applySearchSchema applies the search layer DDL (searchsql) as the owner. Core (FTS) must succeed;
// the vector add-on is best-effort , if the pgvector extension is unavailable, the FTS-only schema and
// reduced health view apply and search.meta records vector=off. Grants run last so they cover
// everything just created.
func (d *DataStore) applySearchSchema(sockDir, port string, c ServicesConfig) error {
	run := func(sql string) error {
		out, err := d.pgCmd(filepath.Dir(sockDir), "psql", "-h", sockDir, "-p", port, "-d", c.Postgres.Name,
			"-v", "ON_ERROR_STOP=1", "-c", sql).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run(searchsql.SchemaCore); err != nil {
		return fmt.Errorf("core: %w", err)
	}
	if err := run(searchsql.SchemaVector); err != nil {
		// pgvector missing (not installed on the box). FTS-only degraded mode, stated, not fatal.
		if e2 := run(searchsql.SchemaNoVector + searchsql.HealthViewNoVector); e2 != nil {
			return fmt.Errorf("no-vector fallback: %w", e2)
		}
	} else if err := run(searchsql.HealthView); err != nil {
		return fmt.Errorf("health view: %w", err)
	}
	if err := run(searchsql.Grants); err != nil {
		return fmt.Errorf("grants: %w", err)
	}
	return nil
}

func (d *DataStore) stopPostgres(slot int, _ ServicesConfig) error {
	data := d.pgData(slot)
	if _, err := os.Stat(filepath.Join(data, "postmaster.pid")); os.IsNotExist(err) {
		return nil // not running
	}
	out, err := d.pgCmd(filepath.Dir(data), "pg_ctl", "-D", data, "-m", "fast", "-w", "stop").CombinedOutput()
	if err != nil {
		return fmt.Errorf("pg_ctl stop slot %d: %v: %s", slot, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (d *DataStore) startRedis(slot int, c ServicesConfig) error {
	dir := d.redisDir(slot)
	// CONVERGE, not start: same rule as Postgres. An authenticated ping answering means this slot's
	// Redis survived whatever restarted secd; re-assert the service ACLs (cheap, idempotent) and done.
	if d.redisCmd(filepath.Dir(dir), "redis-cli", "-p", fmt.Sprint(c.Redis.Port), "-a", c.Redis.Password, "ping").Run() == nil {
		return d.ensureRedisACL(slot, c)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Same as Postgres: secd (root) made this dir, but redis-server runs dropped to the service user
	// and must own its data dir (rdb/aof writes, pid file). chown before starting.
	if cred := d.dbCredential(); cred != nil {
		if err := os.Chown(dir, int(cred.Uid), int(cred.Gid)); err != nil {
			return fmt.Errorf("chown redis dir %s to run user: %w", dir, err)
		}
	}
	pidFile := filepath.Join(dir, "redis.pid")
	// requirepass from services.conf: even loopback-only, an unauthenticated Redis lets any local
	// process read the cache. The password gates it, matching Postgres. The readiness ping below must
	// authenticate too (-a), so a wrong/missing password reads as not-ready, not silently open.
	cmd := d.redisCmd(filepath.Dir(dir), "redis-server",
		"--port", fmt.Sprint(c.Redis.Port),
		"--bind", "127.0.0.1",
		"--dir", dir,
		"--daemonize", "yes",
		"--pidfile", pidFile,
		"--requirepass", c.Redis.Password,
		"--save", "60", "1", // persist to the encrypted volume
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("redis start slot %d: %v: %s", slot, err, strings.TrimSpace(string(out)))
	}
	// brief readiness wait, authenticated
	for i := 0; i < 30; i++ {
		if d.redisCmd(filepath.Dir(dir), "redis-cli", "-p", fmt.Sprint(c.Redis.Port), "-a", c.Redis.Password, "ping").Run() == nil {
			return d.ensureRedisACL(slot, c)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("redis slot %d did not become ready", slot)
}

// ensureRedisACL defines the two service users: ghost_ro (+@read, all keys) and ghost_rw (+@read
// +@write +@keyspace on all keys). The daemons authenticate as one of these; the default user's
// password stays for admin/readiness only. Idempotent , ACL SETUSER overwrites. Run on every start
// (cheap) so a restart re-asserts the ACL even if it was cleared.
func (d *DataStore) ensureRedisACL(slot int, c ServicesConfig) error {
	if c.Redis.ROUser == "" || c.Redis.RWUser == "" {
		return nil // pre-role config; nothing to assert
	}
	mount := filepath.Dir(d.pgData(slot))
	users := [][]string{
		{"ACL", "SETUSER", c.Redis.ROUser, "on", ">" + c.Redis.ROPass, "~*", "+@read", "+ping", "+auth", "+hello"},
		{"ACL", "SETUSER", c.Redis.RWUser, "on", ">" + c.Redis.RWPass, "~*", "+@read", "+@write", "+@keyspace", "+ping", "+auth", "+hello"},
	}
	for _, u := range users {
		args := append([]string{"-p", fmt.Sprint(c.Redis.Port), "-a", c.Redis.Password}, u...)
		if out, err := d.redisCmd(mount, "redis-cli", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("redis acl %s: %v: %s", u[2], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func (d *DataStore) stopRedis(slot int, c ServicesConfig) error {
	port := fmt.Sprint(c.Redis.Port)
	pw := c.Redis.Password
	mount := filepath.Dir(d.pgData(slot))
	if d.redisCmd(mount, "redis-cli", "-p", port, "-a", pw, "ping").Run() != nil {
		return nil // not running (or unreachable) , nothing to stop
	}
	out, err := d.redisCmd(mount, "redis-cli", "-p", port, "-a", pw, "shutdown", "nosave").CombinedOutput()
	// shutdown closes the connection, so an error here is often benign; check it actually stopped.
	if d.redisCmd(mount, "redis-cli", "-p", port, "-a", pw, "ping").Run() == nil {
		return fmt.Errorf("redis slot %d still up after shutdown: %s", slot, strings.TrimSpace(string(out)))
	}
	_ = err
	return nil
}
