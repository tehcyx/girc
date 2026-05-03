package server

import (
	"strings"
	"testing"
	"time"
)

// TestRegistrationNICKThenUSER is the canonical order: NICK before USER.
// This is already covered by TestRegistrationWithoutPASS but we make it
// explicit as the baseline for the table below.
func TestRegistrationNICKThenUSER(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)

	c.send(t, "NICK nickfirst")
	c.send(t, "USER nickfirst 0 * :Nick First")

	line := c.readUntil(t, func(l string) bool { return strings.Contains(l, " 001 ") })
	if !strings.Contains(line, "001") {
		t.Errorf("expected 001 welcome, got: %s", line)
	}
}

// TestRegistrationUSERThenNICK sends USER before NICK.
// The server stores user params and completes registration when NICK arrives.
func TestRegistrationUSERThenNICK(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)

	c.send(t, "USER userfirst 0 * :User First")
	c.send(t, "NICK userfirst")

	line := c.readUntil(t, func(l string) bool { return strings.Contains(l, " 001 ") })
	if !strings.Contains(line, "001") {
		t.Errorf("expected 001 welcome after USER-then-NICK, got: %s", line)
	}
}

// TestRegistrationNICKOnly sends NICK but never USER.
// The server must NOT send 001 — the client should remain unregistered.
func TestRegistrationNICKOnly(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)

	c.send(t, "NICK nickonly")

	// Give the server 200 ms to send any spurious 001.
	got001 := false
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		c.conn.SetReadDeadline(deadline)
		line, err := c.reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, " 001 ") {
			got001 = true
			break
		}
	}
	if got001 {
		t.Error("server sent 001 after NICK only (no USER) — should not register")
	}
}

// TestRegistrationUSEROnly sends USER but never NICK.
// The server must NOT send 001.
func TestRegistrationUSEROnly(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)

	c.send(t, "USER useronly 0 * :User Only")

	got001 := false
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		c.conn.SetReadDeadline(deadline)
		line, err := c.reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, " 001 ") {
			got001 = true
			break
		}
	}
	if got001 {
		t.Error("server sent 001 after USER only (no NICK) — should not register")
	}
}

// TestRegistrationInvalidNICK sends an invalid NICK (with spaces and special chars)
// and expects ERR_ERRONEUSNICKNAME (432).
func TestRegistrationInvalidNICK(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)

	c.send(t, "NICK invalid nick!")

	line := c.readUntil(t, func(l string) bool {
		return strings.Contains(l, " 432 ") || strings.Contains(l, " 431 ") || strings.Contains(l, "Erroneous")
	})
	if !strings.Contains(line, "432") && !strings.Contains(line, "Erroneous") {
		t.Errorf("expected 432 erroneous nickname, got: %s", line)
	}
}

// TestRegistrationInvalidNICKChars verifies that a nick with forbidden characters
// is rejected with 432 ERR_ERRONEUSNICKNAME.
func TestRegistrationInvalidNICKChars(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)

	// "@" is not in the valid charset [a-zA-Z0-9_*&-#]
	c.send(t, "NICK bad@nick")

	line := c.readUntil(t, func(l string) bool {
		return strings.Contains(l, " 432 ") || strings.Contains(l, "Erroneous")
	})
	if !strings.Contains(line, "432") && !strings.Contains(line, "Erroneous") {
		t.Errorf("expected 432 for nick with '@', got: %s", line)
	}
}
