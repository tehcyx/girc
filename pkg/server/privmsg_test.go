package server

import (
	"strings"
	"testing"
	"time"
)

// TestPrivmsgURLColonIntact verifies that a PRIVMSG body containing a URL with
// embedded colons is delivered intact (regression for colon-truncation bug #2).
func TestPrivmsgURLColonIntact(t *testing.T) {
	addr := startTestServer(t)

	sender := dialServer(t, addr)
	receiver := dialServer(t, addr)

	register(t, sender, "urlsender")
	time.Sleep(50 * time.Millisecond)
	register(t, receiver, "urlreceiver")
	time.Sleep(50 * time.Millisecond)

	// Drain welcome messages for receiver before we listen for the PRIVMSG.
	time.Sleep(50 * time.Millisecond)

	const body = "https://example.com/foo:bar?q=1"
	sender.send(t, "PRIVMSG urlreceiver :%s", body)

	line := receiver.readUntil(t, func(l string) bool {
		return strings.Contains(l, "PRIVMSG urlreceiver")
	})
	if !strings.Contains(line, body) {
		t.Errorf("expected body %q intact, got: %s", body, line)
	}
}

// TestPrivmsgScopeChannelOnly verifies that a channel PRIVMSG is delivered
// only to channel members and NOT to unrelated connected clients.
func TestPrivmsgScopeChannelOnly(t *testing.T) {
	addr := startTestServer(t)

	member := dialServer(t, addr)
	outsider := dialServer(t, addr)
	sender := dialServer(t, addr)

	register(t, sender, "chanpsender")
	time.Sleep(50 * time.Millisecond)
	register(t, member, "chanpmember")
	time.Sleep(50 * time.Millisecond)
	register(t, outsider, "chanpoutsider")
	time.Sleep(50 * time.Millisecond)

	// sender and member join #scope; outsider stays in lobby only.
	sender.send(t, "JOIN #scope")
	sender.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "scope") })
	member.send(t, "JOIN #scope")
	member.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "scope") })

	// Drain welcome messages for outsider.
	time.Sleep(50 * time.Millisecond)

	sender.send(t, "PRIVMSG #scope :secret channel message")

	// member must receive it.
	member.readUntil(t, func(l string) bool {
		return strings.Contains(l, "PRIVMSG") && strings.Contains(l, "secret channel message")
	})

	// outsider must NOT receive it within 200 ms.
	gotMsg := false
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		outsider.conn.SetReadDeadline(deadline)
		line, err := outsider.reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, "secret channel message") {
			gotMsg = true
			break
		}
	}
	if gotMsg {
		t.Error("outsider received channel PRIVMSG not intended for them")
	}
}
