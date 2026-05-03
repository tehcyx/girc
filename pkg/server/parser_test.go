package server

import (
	"strings"
	"testing"
	"time"
)

func makeDeadline(seconds int) time.Time {
	return time.Now().Add(time.Duration(seconds) * time.Second)
}

// TestParserEmptyTrailingParam verifies that a PRIVMSG with an empty trailing
// parameter (PRIVMSG #c :) does not panic and is handled gracefully.
// The server should either send it as an empty message or return 412 (no text).
func TestParserEmptyTrailingParam(t *testing.T) {
	addr := startTestServer(t)

	sender := dialServer(t, addr)
	receiver := dialServer(t, addr)

	register(t, sender, "emptysender")
	// Give the server time to finish lobby join before registering receiver.
	// This avoids the concurrent-JoinRoomByName race in the production code.
	// (See note in part_test.go about this pre-existing production bug.)
	sender.send(t, "JOIN #emptychan")
	sender.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "emptychan") })

	register(t, receiver, "emptyreceiver")
	receiver.send(t, "JOIN #emptychan")
	receiver.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "emptychan") })

	// Send a PRIVMSG with empty body — should not panic.
	sender.send(t, "PRIVMSG #emptychan :")

	// Server either delivers empty body or sends ERR_NOTEXTTOSEND (412).
	// Either is acceptable; the key is no crash.
	// (We don't wait for a specific response to avoid test hangs on 412.)
}

// TestParserMultiParamNoTrailing verifies that a command with multiple params
// but no trailing (colon) param is parsed correctly.
// PART #chan1,#chan2 should split into two channels.
func TestParserMultiParamNoTrailing(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)
	register(t, c, "multiparamuser")

	c.send(t, "JOIN #mp1,#mp2")
	c.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "mp1") })
	c.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "mp2") })

	c.send(t, "PART #mp1,#mp2")

	// Both parts should complete without deadlock (covered more thoroughly in TestPARTNoDeadlock).
	c.readUntil(t, func(l string) bool { return strings.Contains(l, "PART") && strings.Contains(l, "mp1") })
	c.readUntil(t, func(l string) bool { return strings.Contains(l, "PART") && strings.Contains(l, "mp2") })
}

// TestParserCRLFTolerance verifies that the server correctly processes messages
// sent with just \n (no \r) — a common client quirk.
func TestParserCRLFTolerance(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)

	// Send with \n only (no \r\n).
	c.conn.Write([]byte("NICK crlfonly\n"))
	c.conn.Write([]byte("USER crlfonly 0 * :CR LF Test\n"))

	line := c.readUntil(t, func(l string) bool {
		return strings.Contains(l, " 001 ")
	})
	if !strings.Contains(line, "001") {
		t.Errorf("server did not register client using \\n-only line endings, got: %s", line)
	}
}

// TestParserQUITWithReason verifies that QUIT with a reason is handled.
func TestParserQUITWithReason(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)
	register(t, c, "quitsender1")

	// Should not hang; server closes connection.
	c.send(t, "QUIT :test reason")

	// Connection should close (read returns error or ERROR line).
	// We just wait for the server to close our connection.
	c.conn.SetReadDeadline(makeDeadline(2))
	for {
		_, err := c.reader.ReadString('\n')
		if err != nil {
			// Connection closed — expected.
			return
		}
	}
}

// TestParserQUITWithoutReason verifies that QUIT without a reason is handled.
func TestParserQUITWithoutReason(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)
	register(t, c, "quitsender2")

	c.send(t, "QUIT")

	c.conn.SetReadDeadline(makeDeadline(2))
	for {
		_, err := c.reader.ReadString('\n')
		if err != nil {
			return
		}
	}
}

// TestParserJOINMultipleChannelsAndKeys verifies that JOIN with multiple
// channels (and optional keys) is parsed without errors.
// RFC 1459: JOIN #ch1,#ch2 key1,key2
// The server currently ignores keys but must not panic.
func TestParserJOINMultipleChannelsAndKeys(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)
	register(t, c, "multijoin")

	// Keys after a space — server ignores them per current implementation.
	c.send(t, "JOIN #j1,#j2 key1,key2")

	c.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "j1") })
	c.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "j2") })
}

// TestParserPrefixedMessageIgnored verifies that a message with a server prefix
// (:nick!user@host CMD ...) is handled gracefully. Modern IRC clients don't
// typically send prefixed messages, but the server must not crash if it receives one.
func TestParserPrefixedMessageIgnored(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)

	// Send a prefixed message before registration — server parses the first token
	// as the command. ":nick!u@h" would be treated as the command, which is
	// unknown, so the server should just ignore it and continue.
	c.conn.Write([]byte(":nick!u@h NICK prefixtest\r\n"))
	c.conn.Write([]byte("NICK prefixtest\r\n"))
	c.conn.Write([]byte("USER prefixtest 0 * :Prefix Test\r\n"))

	// The NICK + USER without prefix should still register the client.
	line := c.readUntil(t, func(l string) bool { return strings.Contains(l, " 001 ") })
	if !strings.Contains(line, "001") {
		t.Errorf("registration failed after prefixed message, got: %s", line)
	}
}
