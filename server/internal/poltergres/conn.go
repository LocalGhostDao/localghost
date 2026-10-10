// Package poltergres is the resident poltergeist's line to Postgres , unseen, permanent, and the one
// that moves things when asked correctly. Concretely: the v3 wire protocol over the in-volume unix
// socket, text format everywhere, one persistent mutex-serialised connection per daemon with
// reconnect-once. It replaces the psql shell-outs and, more importantly, brings parameterized queries
// (extended protocol), so values travel out-of-band and SQL-by-string-concatenation dies.
//
// What it deliberately does NOT implement, because this box never needs it: TLS (the socket lives on
// the encrypted volume), binary result format, COPY, LISTEN/NOTIFY, and connection pooling (query
// volumes here are tiny; a pool solves a problem we do not have).
//
// Auth: trust, cleartext, md5, and SCRAM-SHA-256 , the last is what pg_hba demands for the ghost_ro /
// ghost_rw service roles. pbkdf2 comes from golang.org/x/crypto, already a dependency of this module.
package poltergres

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Conn is one Postgres connection. Callers use ReadOnly/ReadWrite, not this directly.
type Conn struct {
	mu      sync.Mutex
	network string // "unix" or "tcp"
	addr    string // socket path or host:port
	user    string
	pass    string
	db      string
	c       net.Conn
	r       *bufio.Reader
	// readWait is how long one message is waited for (ReadTimeout when zero): a statement that
	// takes longer is given up on, the connection dropped, and the statement NOT sent again
	// (sentError), since the server may still be running it, or may have applied it.
	readWait time.Duration
}

// ReadTimeout is the wait for the server's next message on an ordinary statement; LongTimeout is
// the wait a Long call gives DDL that builds an index over millions of rows.
const (
	ReadTimeout = 30 * time.Second
	LongTimeout = 4 * time.Hour
)

// sentError marks an error that came after the statement had gone to the server: the answer
// never came (a timeout, a dropped socket). Such a statement must not be sent again by the
// reconnect path, a non-idempotent write might be applied twice and a slow DDL started twice.
type sentError struct{ err error }

func (e *sentError) Error() string { return e.err.Error() }
func (e *sentError) Unwrap() error { return e.err }

// drop closes the connection so the next call connects afresh; for a connection whose last
// statement never answered (the server may still be busy with it on that socket).
func (c *Conn) drop() {
	if c.c != nil {
		_ = c.c.Close()
		c.c = nil
	}
}

func newConn(network, addr, user, pass, db string) *Conn {
	return &Conn{network: network, addr: addr, user: user, pass: pass, db: db}
}

// PGError is a server ErrorResponse (severity, SQLSTATE code, message).
type PGError struct {
	Severity string
	Code     string
	Message  string
}

func (e *PGError) Error() string {
	return fmt.Sprintf("pg %s %s: %s", e.Severity, e.Code, e.Message)
}

// connect establishes and authenticates. Caller holds mu.
func (c *Conn) connect() error {
	if c.c != nil {
		_ = c.c.Close()
		c.c = nil
	}
	nc, err := net.DialTimeout(c.network, c.addr, 3*time.Second)
	if err != nil {
		return err
	}
	c.c = nc
	c.r = bufio.NewReader(nc)

	// StartupMessage: the one unframed-type message , int32 length, int32 protocol 3.0, then
	// key\0value\0 pairs, closing NUL.
	var p bytes.Buffer
	binary.Write(&p, binary.BigEndian, int32(196608))
	p.WriteString("user\x00" + c.user + "\x00")
	p.WriteString("database\x00" + c.db + "\x00")
	p.WriteByte(0)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(4+p.Len()))
	if _, err := nc.Write(append(hdr[:], p.Bytes()...)); err != nil {
		return c.fail(err)
	}

	if err := c.authenticate(); err != nil {
		return c.fail(err)
	}
	// Drain ParameterStatus / BackendKeyData until ReadyForQuery.
	for {
		typ, payload, err := c.readMsg()
		if err != nil {
			return c.fail(err)
		}
		switch typ {
		case 'Z':
			return nil
		case 'E':
			return c.fail(parseError(payload))
		case 'S', 'K', 'N': // parameter status, backend key, notice , all ignorable here
		default:
		}
	}
}

func (c *Conn) fail(err error) error {
	if c.c != nil {
		_ = c.c.Close()
		c.c = nil
	}
	return err
}

// authenticate handles the R message sequence: trust (immediate ok), cleartext, md5, or SASL SCRAM.
func (c *Conn) authenticate() error {
	for {
		typ, payload, err := c.readMsg()
		if err != nil {
			return err
		}
		if typ == 'E' {
			return parseError(payload)
		}
		if typ != 'R' {
			return fmt.Errorf("expected auth message, got %q", typ)
		}
		if len(payload) < 4 {
			return errors.New("short auth message")
		}
		code := binary.BigEndian.Uint32(payload[:4])
		switch code {
		case 0: // AuthenticationOk (trust, or after a successful exchange)
			return nil
		case 3: // cleartext
			if err := c.writeMsg('p', append([]byte(c.pass), 0)); err != nil {
				return err
			}
		case 5: // md5: md5(hex(md5(pass+user)) + salt), prefixed "md5"
			if len(payload) < 8 {
				return errors.New("short md5 salt")
			}
			inner := md5.Sum([]byte(c.pass + c.user))
			outer := md5.Sum(append([]byte(hex.EncodeToString(inner[:])), payload[4:8]...))
			resp := "md5" + hex.EncodeToString(outer[:])
			if err := c.writeMsg('p', append([]byte(resp), 0)); err != nil {
				return err
			}
		case 10: // SASL: NUL-separated mechanism list; we speak SCRAM-SHA-256 (not -PLUS, no TLS here)
			if !bytes.Contains(payload[4:], []byte("SCRAM-SHA-256")) {
				return errors.New("server offers no SCRAM-SHA-256")
			}
			if err := c.scramExchange(); err != nil {
				return err
			}
			// loop continues; server sends AuthenticationOk (code 0) next
		default:
			return fmt.Errorf("unsupported auth method %d", code)
		}
	}
}

// scramExchange runs the SCRAM-SHA-256 client flow inside the SASL messages.
func (c *Conn) scramExchange() error {
	sc, err := newScram(c.pass)
	if err != nil {
		return err
	}
	first := sc.clientFirst()
	// SASLInitialResponse: mechanism NUL, int32 response length, response bytes.
	var p bytes.Buffer
	p.WriteString("SCRAM-SHA-256\x00")
	binary.Write(&p, binary.BigEndian, int32(len(first)))
	p.WriteString(first)
	if err := c.writeMsg('p', p.Bytes()); err != nil {
		return err
	}

	typ, payload, err := c.readMsg()
	if err != nil {
		return err
	}
	if typ == 'E' {
		return parseError(payload)
	}
	if typ != 'R' || len(payload) < 4 || binary.BigEndian.Uint32(payload[:4]) != 11 {
		return errors.New("expected SASLContinue")
	}
	final, err := sc.clientFinal(string(payload[4:]))
	if err != nil {
		return err
	}
	if err := c.writeMsg('p', []byte(final)); err != nil {
		return err
	}

	typ, payload, err = c.readMsg()
	if err != nil {
		return err
	}
	if typ == 'E' {
		return parseError(payload)
	}
	if typ != 'R' || len(payload) < 4 || binary.BigEndian.Uint32(payload[:4]) != 12 {
		return errors.New("expected SASLFinal")
	}
	return sc.verifyServer(string(payload[4:])) // the server proves it knew the password too
}

// writeMsg frames one message: type byte, int32 self-inclusive length, payload.
func (c *Conn) writeMsg(typ byte, payload []byte) error {
	hdr := make([]byte, 5)
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(4+len(payload)))
	_ = c.c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.c.Write(hdr); err != nil {
		return err
	}
	_, err := c.c.Write(payload)
	return err
}

// readMsg returns the next message's type and payload.
func (c *Conn) readMsg() (byte, []byte, error) {
	wait := c.readWait
	if wait <= 0 {
		wait = ReadTimeout
	}
	_ = c.c.SetDeadline(time.Now().Add(wait))
	typ, err := c.r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	var lb [4]byte
	if _, err := readFull(c.r, lb[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(lb[:])
	if n < 4 || n > 64<<20 {
		return 0, nil, fmt.Errorf("bad message length %d", n)
	}
	payload := make([]byte, n-4)
	if _, err := readFull(c.r, payload); err != nil {
		return 0, nil, err
	}
	return typ, payload, nil
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// parseError decodes an ErrorResponse's field list (byte code + NUL string, repeated).
func parseError(payload []byte) error {
	e := &PGError{}
	for len(payload) > 0 && payload[0] != 0 {
		code := payload[0]
		rest := payload[1:]
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			break
		}
		val := string(rest[:i])
		switch code {
		case 'S':
			e.Severity = val
		case 'C':
			e.Code = val
		case 'M':
			e.Message = val
		}
		payload = rest[i+1:]
	}
	return e
}

func isNetErr(err error) bool {
	var ne net.Error
	var pe *PGError
	if errors.As(err, &pe) {
		return false // server spoke; the connection is fine, the statement was not
	}
	return errors.As(err, &ne) || errors.Is(err, net.ErrClosed) ||
		strings.Contains(err.Error(), "broken pipe") || strings.Contains(err.Error(), "connection reset") ||
		strings.Contains(err.Error(), "EOF")
}
