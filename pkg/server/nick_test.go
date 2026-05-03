package server

import (
	"strings"
	"testing"
	"time"
)

// TestNICKChangeBroadcastToChannelMembers verifies that a NICK change after
// registration is broadcast to all channel members (not just the user).
func TestNICKChangeBroadcastToChannelMembers(t *testing.T) {
	addr := startTestServer(t)

	changer := dialServer(t, addr)
	observer := dialServer(t, addr)

	register(t, changer, "oldnick2")
	time.Sleep(50 * time.Millisecond)
	register(t, observer, "nickowatcher")
	time.Sleep(50 * time.Millisecond)

	// Both join the same channel.
	changer.send(t, "JOIN #nickchan")
	changer.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "nickchan") })
	observer.send(t, "JOIN #nickchan")
	observer.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "nickchan") })

	changer.send(t, "NICK newnick2")

	// changer themselves should receive the NICK line.
	changer.readUntil(t, func(l string) bool {
		return strings.Contains(l, "NICK") && strings.Contains(l, "newnick2")
	})

	// observer in the same channel should also receive the NICK broadcast.
	line := observer.readUntil(t, func(l string) bool {
		return strings.Contains(l, "NICK") && strings.Contains(l, "newnick2")
	})
	if !strings.Contains(line, "newnick2") {
		t.Errorf("expected NICK broadcast to channel member, got: %s", line)
	}
	if !strings.Contains(line, "oldnick2") {
		t.Errorf("expected NICK broadcast to include old nick 'oldnick2', got: %s", line)
	}
}

// TestNICKChangeBroadcastNoDuplicates verifies that a client in two shared
// channels with another user only receives the NICK change once (deduped).
func TestNICKChangeBroadcastNoDuplicates(t *testing.T) {
	addr := startTestServer(t)

	changer := dialServer(t, addr)
	observer := dialServer(t, addr)

	register(t, changer, "changenick3")
	time.Sleep(50 * time.Millisecond)
	register(t, observer, "watchnick3")
	time.Sleep(50 * time.Millisecond)

	// Both join TWO channels together.
	changer.send(t, "JOIN #nickdup1")
	changer.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "nickdup1") })
	changer.send(t, "JOIN #nickdup2")
	changer.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "nickdup2") })

	observer.send(t, "JOIN #nickdup1")
	observer.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "nickdup1") })
	observer.send(t, "JOIN #nickdup2")
	observer.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "nickdup2") })

	time.Sleep(50 * time.Millisecond)
	changer.send(t, "NICK newnickX")

	// Count how many NICK messages observer receives within 300ms.
	count := 0
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		observer.conn.SetReadDeadline(deadline)
		line, err := observer.reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, "NICK") && strings.Contains(line, "newnickX") {
			count++
		}
	}

	if count == 0 {
		t.Error("observer did not receive NICK broadcast at all")
	}
	if count > 1 {
		t.Errorf("observer received NICK broadcast %d times (expected 1, no duplicates)", count)
	}
}
