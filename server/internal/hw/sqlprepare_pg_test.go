package hw

// Every SQL statement written as a literal anywhere in the server is PREPAREd against a real
// Postgres carrying the full schema a box converges to at unlock (appConfigSchema, the registry,
// the search layer without pgvector). PREPARE parses, resolves every table and column, and infers
// every $n's type without running anything, which is exactly what poltergres's extended-protocol
// Parse asks of the server at run time. So a column renamed in one place, a table that only one
// daemon's code believes in, or a parameter Postgres cannot type fails here instead of in a
// daemon's log on the box , most callers treat a query error as "no rows" and carry on quietly.
//
// Skips unless GHOST_PG_SOCKET_DIR points at a running server (as internal/pgtest does):
//
//	GHOST_PG_SOCKET_DIR=/tmp GHOST_PG_PORT=55432 GHOST_PG_USER=claude go test -run SQLPrepare ./internal/hw/

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/searchsql"
)

// sqlLiteral: a statement, not a fragment of one
var sqlStart = regexp.MustCompile(`^(?s)\s*(SELECT|INSERT INTO|UPDATE|DELETE FROM|WITH)\s`)

// fragments that are completed at run time (a VALUES list built in a loop, an IN (...) or an OR
// chain spliced in): the literal alone is not the statement, so it is not checked
var sqlFragment = regexp.MustCompile(`(?s)(VALUES|\(|\bAND|\bOR|\bWHERE|,|\bIN)\s*$`)

// the pgvector columns and operators: only in a database with the extension (the FTS-only layer
// has none of them), so not checked here
var sqlVector = regexp.MustCompile(`<=>|::vector|\bemb\b|\bemb_model\b|\bembedding\b`)

type sqlSite struct {
	pos string
	sql string
}

// collectSQL walks the repo's non-test Go files for string literals (or + chains of them) that
// read as whole statements.
func collectSQL(t *testing.T, root string) []sqlSite {
	t.Helper()
	var out []sqlSite
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "testdata", ".git", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		seen := map[ast.Node]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil || seen[n] {
				return false
			}
			var s string
			var ok bool
			switch x := n.(type) {
			case *ast.BinaryExpr:
				s, ok = constString(x, seen)
			case *ast.BasicLit:
				s, ok = constString(x, seen)
			default:
				return true
			}
			if !ok {
				return true // a + chain with a variable in it: look inside for whole literals
			}
			if sqlStart.MatchString(s) {
				rel, _ := filepath.Rel(root, fset.Position(n.Pos()).Filename)
				out = append(out, sqlSite{pos: rel + ":" + strconv.Itoa(fset.Position(n.Pos()).Line), sql: s})
			}
			return false
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// constString folds a string literal or a + chain of string literals; ok is false when anything in
// the chain is not a literal.
func constString(e ast.Expr, seen map[ast.Node]bool) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		if err != nil {
			return "", false
		}
		seen[x] = true
		return s, true
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, ok := constString(x.X, seen)
		if !ok {
			return "", false
		}
		r, ok := constString(x.Y, seen)
		if !ok {
			return "", false
		}
		seen[x] = true
		return l + r, true
	case *ast.ParenExpr:
		return constString(x.X, seen)
	}
	return "", false
}

func TestSQLPrepareEveryStatement(t *testing.T) {
	dir := os.Getenv("GHOST_PG_SOCKET_DIR")
	if dir == "" {
		t.Skip("GHOST_PG_SOCKET_DIR not set; no Postgres to test against")
	}
	port := 5432
	if p, err := strconv.Atoi(os.Getenv("GHOST_PG_PORT")); err == nil {
		port = p
	}
	user := os.Getenv("GHOST_PG_USER")
	if user == "" {
		user = "postgres"
	}
	admin := poltergres.NewReadWrite(dir, port, user, "", "postgres")
	if err := admin.Ping(); err != nil {
		t.Fatalf("postgres unreachable: %v", err)
	}
	const name = "lgtest_sqlprepare"
	if err := admin.ExecSimple("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if err := admin.ExecSimple("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", name)
	// what EnsureSchema runs at every unlock, in its order (pgvector absent: the FTS-only layer)
	if err := db.ExecSimple(appConfigSchema); err != nil {
		t.Fatalf("app schema: %v", err)
	}
	if _, err := ConvergeSchema(db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))); err != nil {
		t.Fatalf("converge: %v", err)
	}
	for _, s := range []string{searchsql.SchemaCore, searchsql.SchemaNoVector + searchsql.HealthViewNoVector} {
		if err := db.ExecSimple(s); err != nil {
			t.Fatalf("search schema: %v", err)
		}
	}

	root, _ := filepath.Abs("../..")
	sites := collectSQL(t, root)
	if len(sites) < 50 {
		t.Fatalf("only %d SQL statements found under %s , the walker is broken", len(sites), root)
	}
	checked, skipped := 0, 0
	for _, s := range sites {
		why := ""
		switch {
		case sqlFragment.MatchString(s.sql):
			why = "a fragment completed at run time"
		case sqlVector.MatchString(s.sql):
			why = "pgvector is not in this test database"
		case regexp.MustCompile(`%[-+# 0-9.]*[sdvqx]|%\[`).MatchString(s.sql):
			why = "completed by fmt.Sprintf"
		}
		if why != "" {
			skipped++
			continue
		}
		checked++
		if err := db.ExecSimple("PREPARE lgchk AS " + s.sql); err != nil {
			t.Errorf("%s: %v\n    %s", s.pos, err, oneLine(s.sql))
			continue
		}
		_ = db.ExecSimple("DEALLOCATE lgchk")
	}
	t.Logf("%d statements prepared against the schema, %d skipped (fragments, pgvector, Sprintf)", checked, skipped)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 220 {
		s = s[:220] + "…"
	}
	return s
}
