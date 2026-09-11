// Package daemon is a client for the Nix daemon's worker protocol — the
// interface nix itself talks to nix-daemon. Nyax uses it to add source paths
// and build derivations through the system's store instead of writing to the
// store itself, which keeps store paths and build behaviour identical to Nix.
package daemon

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
)

// The wire constants from Nix's worker-protocol.hh.
const (
	magic1 = 0x6e697863
	magic2 = 0x6478696f

	// protocolVersion is the highest worker protocol we speak. Nix negotiates
	// the minimum of this and the daemon's, so claiming a version below 1.38
	// avoids the feature-set exchange, which Nyax does not need.
	protocolVersion = 1<<8 | 37

	stderrNext          = 0x6f6c6d67
	stderrRead          = 0x64617461
	stderrWrite         = 0x64617416
	stderrLast          = 0x616c7473
	stderrError         = 0x63787470
	stderrStartActivity = 0x53545254
	stderrStopActivity  = 0x53544f50
	stderrResult        = 0x52534c54
)

// Trusted status values.
const (
	trustUnknown = 0
	trustYes     = 1
	trustNo      = 2
)

// Conn is one connection to the daemon. It is not safe for concurrent use.
type Conn struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer

	// version is the negotiated protocol version in Nix's (major << 8 | minor)
	// form. daemonVersion is the daemon's Nix version string, and trusted
	// reports the daemon's verdict on us.
	version       uint64
	daemonVersion string
	trusted       bool

	err error
}

// SocketPath is where the daemon listens: NIX_DAEMON_SOCKET_PATH, else the
// daemon-socket under the state directory.
func SocketPath() string {
	if p := os.Getenv("NIX_DAEMON_SOCKET_PATH"); p != "" {
		return p
	}
	state := os.Getenv("NIX_STATE_DIR")
	if state == "" {
		state = "/nix/var/nix"
	}
	return filepath.Join(state, "daemon-socket", "socket")
}

// Dial connects to the daemon and completes the handshake.
func Dial() (*Conn, error) {
	nc, err := net.Dial("unix", SocketPath())
	if err != nil {
		return nil, err
	}
	c := &Conn{conn: nc, r: bufio.NewReader(nc), w: bufio.NewWriter(nc)}
	if err := c.handshake(); err != nil {
		nc.Close()
		return nil, err
	}
	return c, nil
}

// Close closes the connection.
func (c *Conn) Close() error { return c.conn.Close() }

// Version is the negotiated protocol version, major<<8|minor.
func (c *Conn) Version() uint64 { return c.version }

// DaemonNixVersion is the daemon's Nix version string.
func (c *Conn) DaemonNixVersion() string { return c.daemonVersion }

// Trusted reports whether the daemon granted us trusted access.
func (c *Conn) Trusted() bool { return c.trusted }

// handshake exchanges the greeting and, for protocol 1.38 and later, the
// feature sets.
func (c *Conn) handshake() error {
	c.putUint64(magic1)
	c.putUint64(protocolVersion)
	c.flush()

	if m := c.getUint64(); m != magic2 {
		return c.wrap("nix-daemon protocol mismatch")
	}
	daemon := c.getUint64()
	if daemon>>8 != protocolVersion>>8 {
		return c.wrap("nix-daemon protocol version not supported")
	}
	c.version = daemon
	if protocolVersion < c.version {
		c.version = protocolVersion
	}

	if c.version >= 1<<8|38 {
		// We advertise no features, so we negotiate none.
		c.putUint64(0)
		c.flush()
		if n := c.getUint64(); c.err == nil {
			for i := uint64(0); i < n; i++ {
				c.getString()
			}
		}
	}

	c.postHandshake()
	if c.err != nil {
		return c.wrap("cannot open connection to nix-daemon")
	}
	return nil
}

// postHandshake sends the obsolete fields Nix still requires, reads the
// daemon's handshake info, then drains the startup messages.
func (c *Conn) postHandshake() {
	if c.version >= 1<<8|14 {
		c.putUint64(0) // obsolete CPU affinity
	}
	if c.version >= 1<<8|11 {
		c.putUint64(0) // obsolete reserveSpace
	}
	if c.version >= 1<<8|33 {
		c.flush()
		c.daemonVersion = c.getString()
	}
	if c.version >= 1<<8|35 {
		switch b := c.getUint64(); b {
		case trustYes:
			c.trusted = true
		case trustNo, trustUnknown:
		}
	}
	if c.err == nil {
		c.processStderr()
	} else {
		c.drainStderr()
	}
}

// drainStderr consumes the daemon's stderr stream best-effort, for the case
// where the connection is already failing.
func (c *Conn) drainStderr() {
	for c.err == nil {
		switch c.getMessage() {
		case stderrLast:
			return
		case stderrError:
			return
		}
	}
}

// op sends an operation code and its arguments and flushes.
func (c *Conn) op(code uint64) {
	c.putUint64(code)
}

// finish flushes what an operation wrote and drains the daemon's stderr
// messages, returning any error the daemon reported.
func (c *Conn) finish() error {
	c.flush()
	c.processStderr()
	if c.err != nil {
		return c.cause()
	}
	return nil
}

// processStderr reads the daemon's stderr stream up to STDERR_LAST, handling
// the log, activity and data-transfer messages along the way, and returns the
// daemon's error if it reported one.
func (c *Conn) processStderr() {
	for c.err == nil {
		msg := c.getMessage()
		if c.err != nil {
			return
		}
		switch msg {
		case stderrLast:
			return
		case stderrNext:
			fmt.Fprintln(os.Stderr, c.getString())
		case stderrError:
			e := c.readError()
			if c.err == nil {
				c.err = e
			}
			return
		case stderrWrite:
			// The daemon sends output; drain it.
			n := c.getUint64()
			c.r.Discard(int(n))
			c.skipPadding(n)
		case stderrStartActivity:
			c.getUint64() // activity id
			c.getUint64() // level
			c.getUint64() // type
			c.getString()
			c.readFields()
			c.getUint64() // parent
		case stderrStopActivity:
			c.getUint64()
		case stderrResult:
			c.getUint64() // activity id
			c.getUint64() // type
			c.readFields()
		default:
			c.err = fmt.Errorf("unknown daemon stderr message %#x (version %d.%d, daemon %s)", msg, c.version>>8, c.version&0xff, c.daemonVersion)
			return
		}
	}
}

// readFields reads a Logger::Field list.
func (c *Conn) readFields() {
	n := c.getUint64()
	for i := uint64(0); c.err == nil && i < n; i++ {
		switch c.getUint64() {
		case 0:
			c.getUint64()
		case 1:
			c.getString()
		}
	}
}

// readError reads an error the daemon serialised after STDERR_ERROR.
func (c *Conn) readError() error {
	if c.version < 1<<8|26 {
		msg := c.getString()
		c.getUint64() // status
		return c.wrap("%s", msg)
	}
	if t := c.getString(); t != "Error" {
		return c.wrap("unexpected daemon error type")
	}
	c.getUint64() // level
	c.getString() // name (removed)
	msg := c.getString()
	c.getUint64() // havePos
	for n := c.getUint64(); c.err == nil && n > 0; n-- {
		c.getUint64() // havePos
		c.getString()
	}
	return c.wrap("%s", msg)
}

// ---- wire primitives ----

func (c *Conn) flush() {
	if c.err != nil {
		return
	}
	if err := c.w.Flush(); err != nil {
		c.err = err
	}
}

func (c *Conn) putUint64(v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	if _, err := c.w.Write(b[:]); err != nil && c.err == nil {
		c.err = err
	}
}

func (c *Conn) putString(s string) {
	c.putUint64(uint64(len(s)))
	if _, err := c.w.WriteString(s); err != nil && c.err == nil {
		c.err = err
	}
	c.pad(uint64(len(s)))
}

func (c *Conn) putStrings(ss []string) {
	c.putUint64(uint64(len(ss)))
	for _, s := range ss {
		c.putString(s)
	}
}

func (c *Conn) pad(n uint64) {
	if r := n % 8; r != 0 {
		for i := uint64(0); i < 8-r; i++ {
			if err := c.w.WriteByte(0); err != nil && c.err == nil {
				c.err = err
			}
		}
	}
}

func (c *Conn) getUint64() uint64 {
	var b [8]byte
	if c.err == nil {
		if _, err := io.ReadFull(c.r, b[:]); err != nil {
			c.err = err
		}
	}
	return binary.LittleEndian.Uint64(b[:])
}

func (c *Conn) getString() string {
	n := c.getUint64()
	buf := make([]byte, n)
	if c.err == nil {
		if _, err := io.ReadFull(c.r, buf); err != nil {
			c.err = err
		}
	}
	c.skipPadding(n)
	return string(buf)
}

func (c *Conn) skipPadding(n uint64) {
	if r := n % 8; r != 0 && c.err == nil {
		if _, err := c.r.Discard(int(8 - r)); err != nil {
			c.err = err
		}
	}
}

func (c *Conn) getStrings() []string {
	n := c.getUint64()
	ss := make([]string, 0, n)
	for i := uint64(0); c.err == nil && i < n; i++ {
		ss = append(ss, c.getString())
	}
	return ss
}

// getMessage reads one stderr message tag.
func (c *Conn) getMessage() uint64 { return c.getUint64() }

// cause returns the sticky error and clears it.
func (c *Conn) cause() error {
	err := c.err
	c.err = nil
	return err
}

// wrap annotates the sticky error, or a nil error, with context.
func (c *Conn) wrap(format string, args ...any) error {
	inner := c.cause()
	if inner == nil {
		return fmt.Errorf(format, args...)
	}
	return fmt.Errorf(format+": %w", append(args, inner)...)
}
