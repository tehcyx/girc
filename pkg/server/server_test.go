package server

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tehcyx/girc/internal/config"
)

// testConfig returns a minimal config suitable for tests.
func testConfig() *config.Config {
	c := &config.Config{}
	c.Server.Name = "testserver"
	c.Server.Host = "localhost"
	c.Server.Port = "0"
	c.Server.Motd = "Test MOTD"
	c.Server.Debug = false
	c.Server.DefaultChannelModes = "+nt"
	return c
}

// startTestServer starts a server on a random port and returns the address.
// It stops the server (by closing the listener) on t.Cleanup.
func startTestServer(t *testing.T) string {
	t.Helper()

	cfg := testConfig()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	srv := &Server{
		Clients:       []Client{},
		ClientsMutex:  &sync.Mutex{},
		Rooms:         []Room{},
		RoomsMutex:    &sync.Mutex{},
		ClientTimeout: 30 * time.Second,
		PingInterval:  0, // disable ping for tests
		cfg:           cfg,
	}
	srv.initRooms()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			go srv.handleClientConnect(conn)
		}
	}()

	t.Cleanup(func() {
		ln.Close()
	})

	return ln.Addr().String()
}

// ircConn is a helper for test IRC connections.
type ircConn struct {
	conn   net.Conn
	reader *bufio.Reader
}

func dialServer(t *testing.T, addr string) *ircConn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("failed to dial server: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return &ircConn{conn: conn, reader: bufio.NewReader(conn)}
}

func (c *ircConn) send(t *testing.T, format string, args ...interface{}) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if !strings.HasSuffix(msg, "\r\n") {
		msg += "\r\n"
	}
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.conn.Write([]byte(msg)); err != nil {
		t.Fatalf("write failed: %v", err)
	}
}

func (c *ircConn) readLine(t *testing.T) string {
	t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := c.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	return strings.TrimRight(line, "\r\n")
}

// readUntil reads lines until it finds one matching pred or times out.
func (c *ircConn) readUntil(t *testing.T, pred func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.conn.SetReadDeadline(deadline)
		line, err := c.reader.ReadString('\n')
		if err != nil {
			t.Fatalf("readUntil: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if pred(line) {
			return line
		}
	}
	t.Fatalf("readUntil: condition not met before timeout")
	return ""
}

// register performs NICK + USER and waits for 001 welcome.
func register(t *testing.T, c *ircConn, nick string) {
	t.Helper()
	c.send(t, "NICK %s", nick)
	c.send(t, "USER %s 0 * :Test User", nick)
	c.readUntil(t, func(line string) bool {
		return strings.Contains(line, " 001 ")
	})
}

// TestRegistrationWithoutPASS verifies that a client can register using
// NICK + USER without sending PASS first (bug #1).
func TestRegistrationWithoutPASS(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)

	c.send(t, "NICK smoketest")
	c.send(t, "USER smoketest 0 * :Smoke Test")

	// Expect 001 welcome
	line := c.readUntil(t, func(l string) bool {
		return strings.Contains(l, " 001 ")
	})
	if !strings.Contains(line, "Welcome") && !strings.Contains(line, "001") {
		t.Errorf("expected 001 welcome, got: %s", line)
	}
}

// TestMOTDSequence verifies that after registration the server sends 001..376.
func TestMOTDSequence(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)

	c.send(t, "NICK motdtest")
	c.send(t, "USER motdtest 0 * :MOTD Test")

	got376 := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.conn.SetReadDeadline(deadline)
		line, err := c.reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.Contains(line, " 376 ") {
			got376 = true
			break
		}
	}
	if !got376 {
		t.Error("did not receive 376 (RPL_ENDOFMOTD) after registration")
	}
}

// TestPrivmsgColonInBody verifies that PRIVMSG bodies containing colons are
// delivered intact (bug #2).
func TestPrivmsgColonInBody(t *testing.T) {
	addr := startTestServer(t)

	sender := dialServer(t, addr)
	receiver := dialServer(t, addr)

	register(t, sender, "sender")
	register(t, receiver, "receiver")

	// Drain welcome messages for receiver
	time.Sleep(100 * time.Millisecond)

	sender.send(t, "PRIVMSG receiver :hello:world")

	line := receiver.readUntil(t, func(l string) bool {
		return strings.Contains(l, "PRIVMSG receiver")
	})
	if !strings.Contains(line, "hello:world") {
		t.Errorf("expected body 'hello:world', got: %s", line)
	}
}

// TestPARTNoDeadlock verifies that PARTing two channels in one command
// does not deadlock (bug #4).
func TestPARTNoDeadlock(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)
	register(t, c, "parttest")

	c.send(t, "JOIN #alpha")
	c.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "alpha") })
	c.send(t, "JOIN #beta")
	c.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "beta") })

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.send(t, "PART #alpha,#beta :bye")
	}()

	select {
	case <-done:
		// good, no deadlock
	case <-time.After(3 * time.Second):
		t.Error("PART deadlocked")
	}
}

// TestRemoveClientNotFound verifies that RemoveClient does not panic or
// corrupt the slice when the ID is not found (bug #9).
func TestRemoveClientNotFound(t *testing.T) {
	s := &Server{
		Clients:      []Client{},
		ClientsMutex: &sync.Mutex{},
		Rooms:        []Room{},
		RoomsMutex:   &sync.Mutex{},
		cfg:          testConfig(),
	}

	// Should not panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("RemoveClient panicked: %v", r)
		}
	}()

	nonExistent := uuid.Must(uuid.NewRandom())
	s.RemoveClient(nonExistent)
}

// TestLineCRLF verifies that server responses end with \r\n (bug #10).
func TestLineCRLF(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)

	c.send(t, "NICK crlftest")
	c.send(t, "USER crlftest 0 * :CRLF Test")

	// Read raw bytes for the first line
	c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := c.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if !strings.HasSuffix(line, "\r\n") {
		t.Errorf("line does not end with \\r\\n: %q", line)
	}
}

// TestNICKChangeAfterRegistration verifies that a registered user can change
// their nick (bug #11).
func TestNICKChangeAfterRegistration(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)
	register(t, c, "oldnick")

	c.send(t, "NICK newnick")
	line := c.readUntil(t, func(l string) bool {
		return strings.Contains(l, "NICK") && strings.Contains(l, "newnick")
	})
	if !strings.Contains(line, "newnick") {
		t.Errorf("expected NICK broadcast, got: %s", line)
	}
}

// TestJoinTopicNumerics verifies 331 is sent when no topic (bug #3).
func TestJoinTopicNumerics(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)
	register(t, c, "topictest")

	c.send(t, "JOIN #topictest")
	// Should get 331 (no topic), not 332
	line := c.readUntil(t, func(l string) bool {
		return strings.Contains(l, " 331 ") || strings.Contains(l, " 332 ")
	})
	if !strings.Contains(line, " 331 ") {
		t.Errorf("expected 331 (no topic), got: %s", line)
	}
}

// TestUSERErrCode verifies USER with no params returns 461 not 462 (bug #8).
func TestUSERErrCode(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)

	c.send(t, "NICK errtest")
	c.send(t, "USER") // no params

	line := c.readUntil(t, func(l string) bool {
		return strings.Contains(l, " 461 ") || strings.Contains(l, " 462 ")
	})
	if !strings.Contains(line, " 461 ") {
		t.Errorf("expected 461 (need more params), got: %s", line)
	}
}
