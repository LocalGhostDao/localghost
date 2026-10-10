package poltergres

import (
	"errors"
	"os"
	"strconv"
	"testing"
	"time"
)

// A statement that outlives the read wait is given up on and NOT sent again: the error says it
// was sent, the connection is dropped, and the next statement connects afresh and works. A long
// call waits.
func TestTimedOutStatementIsNotResent(t *testing.T) {
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
	db := NewReadWrite(dir, port, user, "", "postgres")
	if _, err := db.Query("SELECT 1"); err != nil {
		t.Fatal(err)
	}
	db.rw.mu.Lock()
	db.rw.readWait = 500 * time.Millisecond
	db.rw.mu.Unlock()
	t0 := time.Now()
	_, err := db.Query("SELECT pg_sleep(1.5)")
	took := time.Since(t0)
	var se *sentError
	if err == nil || !errors.As(err, &se) {
		t.Fatalf("a timed-out statement: err %v", err)
	}
	if took > 1200*time.Millisecond {
		t.Fatalf("the statement was waited for (or sent) twice: %v", took)
	}
	if db.rw.c != nil {
		t.Fatal("the connection was kept after an unanswered statement")
	}
	db.rw.mu.Lock()
	db.rw.readWait = 0
	db.rw.mu.Unlock()
	if rows, err := db.Query("SELECT 2"); err != nil || len(rows.Vals) != 1 || *rows.Vals[0][0] != "2" {
		t.Fatalf("after the drop: %v %v", err, rows)
	}
	// a long call sits through it
	db.rw.mu.Lock()
	db.rw.readWait = 0
	db.rw.mu.Unlock()
	if err := db.ExecLong("SELECT pg_sleep(0.2)"); err != nil {
		t.Fatalf("long: %v", err)
	}
	if db.rw.readWait != 0 {
		t.Fatal("the long wait was left on the connection")
	}
}
