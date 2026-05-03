package server

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// startPingServer starts a test server with a very short ping interval so the
// PING test doesn't have to wait long.
func startPingServer(t *testing.T, pingInterval time.Duration) string {
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
		PingInterval:  pingInterval,
		cfg:           cfg,
	}
	srv.initRooms()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.handleClientConnect(conn)
		}
	}()

	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

// TestServerPingReceived verifies that after registration the server sends a
// PING to the client within the configured PingInterval.
func TestServerPingReceived(t *testing.T) {
	const pingInterval = 200 * time.Millisecond
	addr := startPingServer(t, pingInterval)

	c := dialServer(t, addr)
	register(t, c, "pingtest")

	// Wait up to 3x the interval for a PING.
	deadline := time.Now().Add(3 * pingInterval)
	gotPing := false
	for time.Now().Before(deadline) {
		c.conn.SetReadDeadline(deadline)
		line, err := c.reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "PING") {
			gotPing = true
			break
		}
	}

	if !gotPing {
		t.Errorf("did not receive PING from server within %v", 3*pingInterval)
	}
}
