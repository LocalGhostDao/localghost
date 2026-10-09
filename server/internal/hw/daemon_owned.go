package hw

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
)

// DaemonOwnedTables are the tables the read-write service role owns rather than the owner role:
// the ones a daemon builds whole by itself, deleting, filling and indexing them as it goes.
// Postgres gives CREATE INDEX and DROP INDEX to a table's owner alone (there is no index
// privilege to grant), and the Wikipedia import takes its lookup indexes off while it reads
// twenty million entries and makes them again at the end; as a table owned by the owner role
// that failed every time ("must be owner of table wiki_articles", seen on 9 October 2026 with
// the whole edition in and no title index). The owner role keeps everything else.
var DaemonOwnedTables = []string{"wiki_articles", "wiki_redirects"}

// daemonOwnedList is the tables as a SQL list: 'wiki_articles', 'wiki_redirects'.
func daemonOwnedList() string {
	q := make([]string, len(DaemonOwnedTables))
	for i, t := range DaemonOwnedTables {
		q[i] = pgLit(t)
	}
	return strings.Join(q, ", ")
}

// ensureDaemonOwned hands DaemonOwnedTables to the read-write role, as the superuser, once they
// exist (the converge creates them as the owner role on the first unlock; this runs after it).
// The owner role is given every privilege on them with the grant option, every unlock: its
// blanket GRANT ... ON ALL TABLES that follows is an error, not a warning, on a table it has
// no privilege on at all (the first unlock with this in place failed at starting the database
// that way, 9 October 2026), and with the grant option its grants to the service roles go
// through as before. The read-only role keeps SELECT. The ownership change itself only
// touches a table whose owner is wrong, so a converged database takes no table lock for it.
// The read-write role also gets CREATE on schema public: an index is a new object in the schema,
// and owning the table is not enough without it (PG15 and later grant CREATE on public to no
// one). A failure is said, not fatal. Proved against Postgres 16 on 9 October 2026: without the
// owner's grant option the blanket grant errors, without CREATE on the schema the index does.
func (d *DataStore) ensureDaemonOwned(slot int, c ServicesConfig) {
	if c.Postgres.RWUser == "" || c.Postgres.ROUser == "" {
		return
	}
	data := d.pgData(slot)
	port := strconv.Itoa(c.Postgres.Port)
	sql := fmt.Sprintf(`
SET lock_timeout = '15s';
GRANT CREATE ON SCHEMA public TO %[3]s;
DO $$ DECLARE r record; BEGIN
  FOR r IN SELECT tablename AS n, tableowner AS o FROM pg_tables WHERE schemaname = 'public' AND tablename IN (%[1]s) ORDER BY 1 LOOP
    IF r.o <> %[2]s THEN
      EXECUTE format('ALTER TABLE public.%%I OWNER TO %[3]s', r.n);
    END IF;
    EXECUTE format('GRANT ALL ON public.%%I TO %[5]s WITH GRANT OPTION', r.n);
    EXECUTE format('GRANT SELECT ON public.%%I TO %[4]s', r.n);
  END LOOP;
END $$;`, daemonOwnedList(), pgLit(c.Postgres.RWUser), c.Postgres.RWUser, c.Postgres.ROUser, c.Postgres.User)
	out, err := d.pgCmd(filepath.Dir(data), "psql", "-h", data, "-p", port, "-d", c.Postgres.Name, "-tA",
		"-v", "ON_ERROR_STOP=1", "-c", sql).CombinedOutput()
	if err != nil {
		slog.Warn("the daemon-owned tables keep their owners, unlock goes on", "fn", "ensureDaemonOwned", "err", err, "out", strings.TrimSpace(string(out)))
	}
}
