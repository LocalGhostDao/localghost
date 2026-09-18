package poltergres

// Query execution. The rule of the house: EVERY value from outside the binary , user text, file
// content, ids from another daemon , goes through Query/Exec as a $n parameter. QuerySimple exists
// for exactly two callers (documented at their sites) and admits only values gated by validators.
// Two paths:
//
//   Exec/Query with args -> the EXTENDED protocol (Parse/Bind/Describe/Execute/Sync). Parameters travel
//   as their own length-prefixed values, NEVER interpolated into SQL text, so injection is not a thing
//   we reason about , it is structurally impossible. This is the whole reason for writing poltergres.
//
//   All results come back in TEXT format (result format code 0), so a caller parses strings exactly
//   like the old `psql -t -A` output did , no binary decoders to maintain.
//
// Role typing: ReadOnly exposes only Query (SELECT). ReadWrite adds Exec (INSERT/UPDATE/DELETE/DDL).
// The Postgres GRANTs on ghost_ro / ghost_rw are the real enforcement; the types make a read-only
// daemon unable to even name a write.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
)

// Rows is a text-format result set. Cols are column names; Vals[r][c] is a cell, with a nil marking
// SQL NULL (distinct from empty string).
type Rows struct {
	Cols []string
	Vals [][]*string
}

// query runs one parameterized statement via the extended protocol and collects rows. Caller holds mu.
func (c *Conn) query(sql string, args []any) (*Rows, error) {
	if err := c.sendExtended(sql, args); err != nil {
		return nil, err
	}
	var rows Rows
	for {
		typ, payload, err := c.readMsg()
		if err != nil {
			return nil, err
		}
		switch typ {
		case '1', '2', 'n', 't': // ParseComplete, BindComplete, NoData, ParameterDescription , skip
		case 'T': // RowDescription
			rows.Cols = parseRowDescription(payload)
		case 'D': // DataRow
			rows.Vals = append(rows.Vals, parseDataRow(payload))
		case 'C': // CommandComplete
		case 'Z': // ReadyForQuery , end of the exchange
			return &rows, nil
		case 'E':
			perr := parseError(payload)
			c.drainToReady()
			return nil, perr
		default:
		}
	}
}

// sendExtended writes Parse (unnamed), Bind (unnamed, all-text params + results), Execute, Sync.
func (c *Conn) sendExtended(sql string, args []any) error {
	// Parse: name\0 query\0 int16 paramTypeCount(0 = infer)
	var parse bytes.Buffer
	parse.WriteByte(0)
	parse.WriteString(sql)
	parse.WriteByte(0)
	binary.Write(&parse, binary.BigEndian, int16(0))
	if err := c.writeMsg('P', parse.Bytes()); err != nil {
		return err
	}

	// Bind: portal\0 stmt\0 int16 paramFormatCount=0(all text) int16 paramCount [int32 len + bytes]...
	// int16 resultFormatCount=0 (all text)
	var bind bytes.Buffer
	bind.WriteByte(0) // portal
	bind.WriteByte(0) // statement
	binary.Write(&bind, binary.BigEndian, int16(0))
	binary.Write(&bind, binary.BigEndian, int16(len(args)))
	for _, a := range args {
		if a == nil {
			binary.Write(&bind, binary.BigEndian, int32(-1)) // SQL NULL
			continue
		}
		s := argText(a)
		binary.Write(&bind, binary.BigEndian, int32(len(s)))
		bind.WriteString(s)
	}
	binary.Write(&bind, binary.BigEndian, int16(0)) // result formats: all text
	if err := c.writeMsg('B', bind.Bytes()); err != nil {
		return err
	}

	// Execute: portal\0 int32 maxRows(0 = all)
	var exec bytes.Buffer
	exec.WriteByte(0)
	binary.Write(&exec, binary.BigEndian, int32(0))
	if err := c.writeMsg('E', exec.Bytes()); err != nil {
		return err
	}
	return c.writeMsg('S', nil) // Sync
}

// drainToReady consumes messages until ReadyForQuery after an error, so the connection is reusable.
func (c *Conn) drainToReady() {
	for {
		typ, _, err := c.readMsg()
		if err != nil || typ == 'Z' {
			return
		}
	}
}

// argText renders a Go value as Postgres text-format input. bool -> t/f matches Postgres literal input.
// []byte is bytea hex input ("\x..."), the same encoding the search store's hexArg hand-rolls , the
// default fmt.Sprint branch used to turn a byte slice into "[12 34 56]", a value that matched no
// bytea and no text column and raised no error (found the hard way: a WHERE hash = $2 that never hit).
func argText(a any) string {
	switch v := a.(type) {
	case string:
		return v
	case []byte:
		return "\\x" + hex.EncodeToString(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		if v {
			return "t"
		}
		return "f"
	default:
		return fmt.Sprint(v)
	}
}

func parseRowDescription(p []byte) []string {
	if len(p) < 2 {
		return nil
	}
	n := int(binary.BigEndian.Uint16(p[:2]))
	p = p[2:]
	cols := make([]string, 0, n)
	for i := 0; i < n; i++ {
		j := bytes.IndexByte(p, 0)
		if j < 0 {
			break
		}
		cols = append(cols, string(p[:j]))
		p = p[j+1:]
		if len(p) < 18 { // tableOID(4) col(2) typeOID(4) typeLen(2) typeMod(4) format(2)
			break
		}
		p = p[18:]
	}
	return cols
}

func parseDataRow(p []byte) []*string {
	if len(p) < 2 {
		return nil
	}
	n := int(binary.BigEndian.Uint16(p[:2]))
	p = p[2:]
	vals := make([]*string, 0, n)
	for i := 0; i < n; i++ {
		if len(p) < 4 {
			break
		}
		l := int32(binary.BigEndian.Uint32(p[:4]))
		p = p[4:]
		if l < 0 {
			vals = append(vals, nil) // NULL
			continue
		}
		if int(l) > len(p) {
			break
		}
		s := string(p[:l])
		vals = append(vals, &s)
		p = p[l:]
	}
	return vals
}

// run is the connection-managing wrapper: connect if needed, retry once on a transport error.
func (c *Conn) run(sql string, args []any) (*Rows, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.c == nil {
		if err := c.connect(); err != nil {
			return nil, err
		}
	}
	rows, err := c.query(sql, args)
	if err != nil && isNetErr(err) {
		if cerr := c.connect(); cerr != nil {
			return nil, cerr
		}
		return c.query(sql, args)
	}
	return rows, err
}

// --- role-typed clients ---

// ReadOnly can only SELECT. Authenticate it as ghost_ro (GRANT SELECT only), so even a crafted SQL
// string reaching Query cannot write , the server rejects it.
type ReadOnly struct{ c *Conn }

// ReadWrite adds Exec. Authenticate as ghost_rw.
type ReadWrite struct {
	ReadOnly
	rw *Conn
}

// SocketDir is the pg socket directory (<mount>/postgres); Postgres names the socket file
// .s.PGSQL.<port> inside it, which net.Dial needs spelled out.
func socketPath(dir string, port int) string {
	return fmt.Sprintf("%s/.s.PGSQL.%d", dir, port)
}

// NewReadOnly connects (lazily) as the read-only role over the unix socket.
func NewReadOnly(socketDir string, port int, user, pass, db string) *ReadOnly {
	return &ReadOnly{c: newConn("unix", socketPath(socketDir, port), user, pass, db)}
}

// NewReadWrite connects (lazily) as the read-write role.
func NewReadWrite(socketDir string, port int, user, pass, db string) *ReadWrite {
	c := newConn("unix", socketPath(socketDir, port), user, pass, db)
	return &ReadWrite{ReadOnly: ReadOnly{c: c}, rw: c}
}

// Query runs a SELECT with parameters ($1,$2,...). Available on both clients.
func (r *ReadOnly) Query(sql string, args ...any) (*Rows, error) { return r.c.run(sql, args) }

// Ping verifies the connection and role by asking the server for 1.
func (r *ReadOnly) Ping() error {
	_, err := r.c.run("SELECT 1", nil)
	return err
}

// QuerySimple runs one or more ;-separated statements over the SIMPLE protocol and returns the LAST
// result set. No parameters , the caller must only inline values it fully controls (validated enums,
// integers, hex, vector literals). It exists for the one case the extended protocol cannot express:
// SET LOCAL + SELECT sharing the implicit transaction of a single simple-query message (pgvector's
// per-query hnsw.ef_search / iterative_scan settings, spec 7.2).
func (r *ReadOnly) QuerySimple(sql string) (*Rows, error) {
	r.c.mu.Lock()
	defer r.c.mu.Unlock()
	if r.c.c == nil {
		if err := r.c.connect(); err != nil {
			return nil, err
		}
	}
	rows, err := r.c.querySimpleLocked(sql)
	if err != nil && isNetErr(err) {
		if cerr := r.c.connect(); cerr != nil {
			return nil, cerr
		}
		return r.c.querySimpleLocked(sql)
	}
	return rows, err
}

func (c *Conn) querySimpleLocked(sql string) (*Rows, error) {
	if err := c.writeMsg('Q', append([]byte(sql), 0)); err != nil {
		return nil, err
	}
	var rows Rows
	for {
		typ, payload, err := c.readMsg()
		if err != nil {
			return nil, err
		}
		switch typ {
		case 'T':
			rows = Rows{Cols: parseRowDescription(payload)} // keep only the last result set
		case 'D':
			rows.Vals = append(rows.Vals, parseDataRow(payload))
		case 'C', 'I', 'N', 'S':
		case 'E':
			perr := parseError(payload)
			c.drainToReady()
			return nil, perr
		case 'Z':
			return &rows, nil
		}
	}
}

// Exec runs a writing statement (INSERT/UPDATE/DELETE/DDL) with parameters. Write client only.
func (w *ReadWrite) Exec(sql string, args ...any) error {
	_, err := w.rw.run(sql, args)
	return err
}

// ExecSimple runs a statement with NO parameters over the simple-query path , for multi-statement DDL
// (schema application) where the extended protocol's one-statement-per-Parse rule is awkward. Write
// client only; still authenticated as ghost_rw. No user input goes here , DDL is ours, static.
func (w *ReadWrite) ExecSimple(sql string) error {
	w.rw.mu.Lock()
	defer w.rw.mu.Unlock()
	if w.rw.c == nil {
		if err := w.rw.connect(); err != nil {
			return err
		}
	}
	if err := w.rw.writeMsg('Q', append([]byte(sql), 0)); err != nil {
		return err
	}
	for {
		typ, payload, err := w.rw.readMsg()
		if err != nil {
			return err
		}
		switch typ {
		case 'Z':
			return nil
		case 'E':
			perr := parseError(payload)
			w.rw.drainToReady()
			return perr
		}
	}
}
