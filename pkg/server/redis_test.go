package server

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	iredis "github.com/tehcyx/girc/pkg/redis"
)

// makeRedisServer creates a test server backed by miniredis.
// It starts the Redis event subscription goroutine and registers cleanup.
func makeRedisServer(t *testing.T, cfg *testServerConfig, mr *miniredis.Miniredis, podID string) (*Server, string) {
	t.Helper()

	scfg := testConfig()
	scfg.Redis.Enabled = true
	scfg.Redis.URL = "redis://" + mr.Addr()
	scfg.Redis.PodID = podID

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	rc, err := iredis.NewClient(scfg.Redis.URL, podID)
	if err != nil {
		t.Fatalf("redis client %s: %v", podID, err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	srv := &Server{
		Clients:       []Client{},
		ClientsMutex:  &sync.Mutex{},
		Rooms:         []Room{},
		RoomsMutex:    &sync.Mutex{},
		ClientTimeout: 30 * time.Second,
		PingInterval:  0,
		cfg:           scfg,
		RedisClient:   rc,
		redisCtx:      ctx,
		redisCancel:   cancel,
	}
	srv.initRooms()
	go srv.subscribeToRedisEvents()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.handleClientConnect(conn)
		}
	}()

	t.Cleanup(func() {
		cancel()
		rc.Close()
		ln.Close()
	})

	return srv, ln.Addr().String()
}

// testServerConfig is a placeholder to make the helper signature clear.
type testServerConfig struct{}

// TestRedisCrossPodPrivmsg verifies that a PRIVMSG received from another pod via
// Redis pub/sub is delivered to the local client (simulating distributed mode).
func TestRedisCrossPodPrivmsg(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	// Server B (pod-B) is the receiver.
	srvB, addrB := makeRedisServer(t, nil, mr, "pod-B")

	// Connect a client to srvB.
	conn, err := net.DialTimeout("tcp", addrB, 5*time.Second)
	if err != nil {
		t.Fatalf("dial srvB: %v", err)
	}
	defer conn.Close()

	ic := &ircConn{conn: conn, reader: bufio.NewReader(conn)}
	register(t, ic, "receiverB")

	// Simulate a PRIVMSG arriving from pod A via Redis pub/sub.
	// We call the handler directly to avoid relying on miniredis pub/sub timing.
	event := &iredis.Event{
		Type: "PRIVMSG",
		Data: map[string]interface{}{
			"from_nick": "senderA",
			"from_user": "senderA",
			"target":    "receiverB",
			"message":   "hello from pod A",
			"pod_id":    "pod-A", // different pod — must not be filtered
		},
	}
	srvB.handleRedisPrivmsg(event)

	// The client on srvB should receive the message.
	line := ic.readUntil(t, func(l string) bool {
		return strings.Contains(l, "PRIVMSG") && strings.Contains(l, "hello from pod A")
	})
	if !strings.Contains(line, "hello from pod A") {
		t.Errorf("expected cross-pod PRIVMSG, got: %s", line)
	}
}

// TestRedisCrossPodNick verifies that a NICK change from another pod is forwarded
// to local clients.
func TestRedisCrossPodNick(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	srvB, addrB := makeRedisServer(t, nil, mr, "pod-B")

	conn, err := net.DialTimeout("tcp", addrB, 5*time.Second)
	if err != nil {
		t.Fatalf("dial srvB: %v", err)
	}
	defer conn.Close()

	ic := &ircConn{conn: conn, reader: bufio.NewReader(conn)}
	register(t, ic, "localUser")

	// Simulate a NICK event from pod A.
	event := &iredis.Event{
		Type: "NICK",
		Data: map[string]interface{}{
			"old_nick": "oldRemoteNick",
			"new_nick": "newRemoteNick",
			"user":     "remoteUser",
			"pod_id":   "pod-A",
		},
	}
	srvB.handleRedisNick(event)

	// localUser should receive the NICK broadcast.
	line := ic.readUntil(t, func(l string) bool {
		return strings.Contains(l, "NICK") && strings.Contains(l, "newRemoteNick")
	})
	if !strings.Contains(line, "newRemoteNick") {
		t.Errorf("expected NICK broadcast, got: %s", line)
	}
}
