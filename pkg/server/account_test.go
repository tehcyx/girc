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

// startAccountServer starts a test server with miniredis for account commands.
func startAccountServer(t *testing.T) (string, *miniredis.Miniredis) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}

	cfg := testConfig()
	cfg.Redis.Enabled = true
	cfg.Redis.URL = "redis://" + mr.Addr()
	cfg.Redis.PodID = "test-pod"

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	rc, err := iredis.NewClient(cfg.Redis.URL, "test-pod")
	if err != nil {
		t.Fatalf("redis client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Clients:       []Client{},
		ClientsMutex:  &sync.Mutex{},
		Rooms:         []Room{},
		RoomsMutex:    &sync.Mutex{},
		ClientTimeout: 30 * time.Second,
		PingInterval:  0,
		cfg:           cfg,
		RedisClient:   rc,
		redisCtx:      ctx,
		redisCancel:   cancel,
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

	t.Cleanup(func() {
		cancel()
		rc.Close()
		ln.Close()
		mr.Close()
	})

	return ln.Addr().String(), mr
}

// TestREGISTERCommand verifies that REGISTER stores a user account in Redis.
func TestREGISTERCommand(t *testing.T) {
	addr, _ := startAccountServer(t)

	c := dialServer(t, addr)
	// Register a user account before doing NICK/USER (REGISTER doesn't require registration).
	c.send(t, "REGISTER testuser Password123! test@example.com")

	// Should receive 902 RplRegistered.
	line := c.readUntil(t, func(l string) bool {
		return strings.Contains(l, " 902 ") || strings.Contains(l, "Account created")
	})
	if !strings.Contains(line, "902") && !strings.Contains(line, "Account created") {
		t.Errorf("expected 902 registered reply, got: %s", line)
	}
}

// TestREGISTERDuplicate verifies that re-registering an existing account returns an error.
func TestREGISTERDuplicate(t *testing.T) {
	addr, _ := startAccountServer(t)

	c1 := dialServer(t, addr)
	c1.send(t, "REGISTER dupuser Password123! dup@example.com")
	c1.readUntil(t, func(l string) bool { return strings.Contains(l, "902") || strings.Contains(l, "Account created") })

	c2 := dialServer(t, addr)
	c2.send(t, "REGISTER dupuser Password123! dup2@example.com")
	line := c2.readUntil(t, func(l string) bool {
		return strings.Contains(l, "903") || strings.Contains(l, "already registered")
	})
	if !strings.Contains(line, "903") && !strings.Contains(line, "already registered") {
		t.Errorf("expected 903 already-registered error, got: %s", line)
	}
}

// TestWHOACCNotLoggedIn verifies that WHOACC returns "not logged in" for unauthenticated users.
func TestWHOACCNotLoggedIn(t *testing.T) {
	addr, _ := startAccountServer(t)

	c := dialServer(t, addr)
	// Drain connection buffers after registration, then send WHOACC.
	// Read as many lines as available without blocking.
	register(t, c, "whoacctest")

	// Need to drain the lobby JOIN, MOTD etc.
	time.Sleep(50 * time.Millisecond)

	c.send(t, "WHOACC")
	line := c.readUntil(t, func(l string) bool {
		return strings.Contains(l, "NOTICE") && strings.Contains(l, "not logged in")
	})
	if !strings.Contains(line, "not logged in") {
		t.Errorf("expected 'not logged in' notice, got: %s", line)
	}
}

// TestSETPASSNotAuthenticated verifies that SETPASS fails if user is not authenticated.
func TestSETPASSNotAuthenticated(t *testing.T) {
	addr, _ := startAccountServer(t)

	c := dialServer(t, addr)
	register(t, c, "setpasstest")
	time.Sleep(50 * time.Millisecond)

	c.send(t, "SETPASS oldpass newpassword123")
	line := c.readUntil(t, func(l string) bool {
		return strings.Contains(l, "906") || strings.Contains(l, "logged in")
	})
	if !strings.Contains(line, "906") && !strings.Contains(line, "logged in") {
		t.Errorf("expected 906 account-required error, got: %s", line)
	}
}

// TestREGISTERThenAuthenticate verifies the full registration + login flow:
// REGISTER creates an account; PASS+NICK+USER authenticates on the next connection.
func TestREGISTERThenAuthenticate(t *testing.T) {
	addr, _ := startAccountServer(t)

	// Connection 1: register the account.
	conn1, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn1.Close()
	c1 := &ircConn{conn: conn1, reader: bufio.NewReader(conn1)}
	c1.send(t, "REGISTER authuser Password123! auth@example.com")
	c1.readUntil(t, func(l string) bool { return strings.Contains(l, "902") || strings.Contains(l, "Account created") })

	// Connection 2: log in with PASS before NICK/USER.
	conn2, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial2: %v", err)
	}
	defer conn2.Close()
	c2 := &ircConn{conn: conn2, reader: bufio.NewReader(conn2)}
	c2.send(t, "PASS Password123!")
	c2.send(t, "NICK authuser")
	c2.send(t, "USER authuser 0 * :Auth User")

	// Should receive 001 welcome and 900 logged-in.
	got001 := false
	got900 := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn2.SetReadDeadline(deadline)
		line, err := c2.reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.Contains(line, " 001 ") {
			got001 = true
		}
		if strings.Contains(line, " 900 ") {
			got900 = true
		}
		if got001 && got900 {
			break
		}
	}
	if !got001 {
		t.Error("did not receive 001 welcome after login")
	}
	if !got900 {
		t.Error("did not receive 900 logged-in after authentication")
	}
}
