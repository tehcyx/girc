package server

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	iredis "github.com/tehcyx/girc/pkg/redis"
)

// TestRedisCrossPodChannelPrivmsg verifies that a PRIVMSG to a channel
// received from another pod via Redis is delivered to local channel members.
// This supplements TestRedisCrossPodPrivmsg which only covers DM delivery.
func TestRedisCrossPodChannelPrivmsg(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	// Server B has a local client in #mychan.
	srvB, addrB := makeRedisServer(t, mr, "pod-B")

	conn, err := net.DialTimeout("tcp", addrB, 5*time.Second)
	if err != nil {
		t.Fatalf("dial srvB: %v", err)
	}
	defer conn.Close()

	ic := &ircConn{conn: conn, reader: bufio.NewReader(conn)}
	register(t, ic, "chanmember")

	// Join the channel on srvB.
	ic.send(t, "JOIN #mychan")
	ic.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "mychan") })

	// Inject a channel PRIVMSG from pod-A directly (bypassing pub/sub timing).
	event := &iredis.Event{
		Type: "PRIVMSG",
		Data: map[string]interface{}{
			"from_nick": "remoteA",
			"from_user": "remoteA",
			"target":    "#mychan",
			"message":   "hello channel from pod A",
			"pod_id":    "pod-A",
		},
	}
	srvB.handleRedisPrivmsg(event)

	line := ic.readUntil(t, func(l string) bool {
		return strings.Contains(l, "PRIVMSG") && strings.Contains(l, "hello channel from pod A")
	})
	if !strings.Contains(line, "hello channel from pod A") {
		t.Errorf("expected cross-pod channel PRIVMSG, got: %s", line)
	}
	if !strings.Contains(line, "#mychan") {
		t.Errorf("expected target #mychan in PRIVMSG, got: %s", line)
	}
}

// TestRedisCrossPodJoin verifies that when a client on pod A joins #x,
// clients on pod B who are already in #x receive the JOIN notification.
func TestRedisCrossPodJoin(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	srvB, addrB := makeRedisServer(t, mr, "pod-B")

	conn, err := net.DialTimeout("tcp", addrB, 5*time.Second)
	if err != nil {
		t.Fatalf("dial srvB: %v", err)
	}
	defer conn.Close()

	ic := &ircConn{conn: conn, reader: bufio.NewReader(conn)}
	register(t, ic, "localInChan")

	ic.send(t, "JOIN #shared")
	ic.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "shared") })

	// Simulate a remote client on pod-A joining #shared.
	event := &iredis.Event{
		Type: "JOIN",
		Data: map[string]interface{}{
			"uuid":    "remote-uuid-1",
			"nick":    "remoteJoiner",
			"user":    "remoteJoiner",
			"channel": "#shared",
			"pod_id":  "pod-A",
		},
	}
	srvB.handleRedisJoin(event)

	line := ic.readUntil(t, func(l string) bool {
		return strings.Contains(l, "JOIN") && strings.Contains(l, "remoteJoiner")
	})
	if !strings.Contains(line, "remoteJoiner") {
		t.Errorf("expected cross-pod JOIN notification, got: %s", line)
	}
}

// TestRedisCrossPodPart verifies that when a client on pod A parts #x,
// clients on pod B in #x receive the PART notification.
func TestRedisCrossPodPart(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	srvB, addrB := makeRedisServer(t, mr, "pod-B")

	conn, err := net.DialTimeout("tcp", addrB, 5*time.Second)
	if err != nil {
		t.Fatalf("dial srvB: %v", err)
	}
	defer conn.Close()

	ic := &ircConn{conn: conn, reader: bufio.NewReader(conn)}
	register(t, ic, "localPartObs")

	ic.send(t, "JOIN #partchan")
	ic.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "partchan") })

	// Simulate remote PART from pod-A.
	event := &iredis.Event{
		Type: "PART",
		Data: map[string]interface{}{
			"uuid":    "remote-uuid-2",
			"nick":    "remotePartier",
			"user":    "remotePartier",
			"channel": "#partchan",
			"message": "bye",
			"pod_id":  "pod-A",
		},
	}
	srvB.handleRedisPart(event)

	line := ic.readUntil(t, func(l string) bool {
		return strings.Contains(l, "PART") && strings.Contains(l, "remotePartier")
	})
	if !strings.Contains(line, "remotePartier") {
		t.Errorf("expected cross-pod PART notification, got: %s", line)
	}
}

// TestRedisCrossPodQuit verifies that when a client on pod A QUITs,
// local clients on pod B receive the QUIT notification.
func TestRedisCrossPodQuit(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	srvB, addrB := makeRedisServer(t, mr, "pod-B")

	conn, err := net.DialTimeout("tcp", addrB, 5*time.Second)
	if err != nil {
		t.Fatalf("dial srvB: %v", err)
	}
	defer conn.Close()

	ic := &ircConn{conn: conn, reader: bufio.NewReader(conn)}
	register(t, ic, "localQuitObs")

	// Simulate remote QUIT from pod-A.
	event := &iredis.Event{
		Type: "QUIT",
		Data: map[string]interface{}{
			"uuid":   "remote-uuid-3",
			"nick":   "remoteQuitter",
			"user":   "remoteQuitter",
			"reason": "Connection closed",
			"pod_id": "pod-A",
		},
	}
	srvB.handleRedisQuit(event)

	line := ic.readUntil(t, func(l string) bool {
		return strings.Contains(l, "QUIT") && strings.Contains(l, "remoteQuitter")
	})
	if !strings.Contains(line, "remoteQuitter") {
		t.Errorf("expected cross-pod QUIT notification, got: %s", line)
	}
}

// TestRedisLoopGuard verifies that events with the same pod_id as the receiver
// are NOT re-delivered (no echo loop). A PRIVMSG from "pod-B" must not be
// delivered to local clients on pod-B.
func TestRedisLoopGuard(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	srvB, addrB := makeRedisServer(t, mr, "pod-B")

	conn, err := net.DialTimeout("tcp", addrB, 5*time.Second)
	if err != nil {
		t.Fatalf("dial srvB: %v", err)
	}
	defer conn.Close()

	ic := &ircConn{conn: conn, reader: bufio.NewReader(conn)}
	register(t, ic, "loopguardtest")

	// Inject a PRIVMSG claiming to be from the SAME pod (pod-B) — must be filtered.
	event := &iredis.Event{
		Type: "PRIVMSG",
		Data: map[string]interface{}{
			"from_nick": "senderB",
			"from_user": "senderB",
			"target":    "loopguardtest",
			"message":   "loopguard message",
			"pod_id":    "pod-B", // same as srvB's pod ID — must be filtered
		},
	}
	srvB.handleRedisPrivmsg(event)

	// The loopguard message must NOT arrive within 200 ms.
	gotMsg := false
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		ic.conn.SetReadDeadline(deadline)
		line, err := ic.reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, "loopguard message") {
			gotMsg = true
			break
		}
	}
	if gotMsg {
		t.Error("same-pod PRIVMSG was not filtered (loop guard failed)")
	}
}
