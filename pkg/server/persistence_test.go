package server

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	iredis "github.com/tehcyx/girc/pkg/redis"
)

// startPersistenceServer creates a Redis-backed test server that can be restarted.
// Returns the server and a function to create a new server connected to the same Redis.
func startPersistenceServer(t *testing.T, mr *miniredis.Miniredis) (string, func() string) {
	t.Helper()

	cfg := testConfig()
	cfg.Redis.Enabled = true
	cfg.Redis.URL = "redis://" + mr.Addr()
	cfg.Redis.PodID = "persist-pod"

	makeServer := func() string {
		rc, err := iredis.NewClient(cfg.Redis.URL, "persist-pod")
		if err != nil {
			t.Fatalf("redis client: %v", err)
		}

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}

		ctxSrv, cancel := context.WithCancel(context.Background())
		srv := &Server{
			Clients:       []Client{},
			ClientsMutex:  &sync.Mutex{},
			Rooms:         []Room{},
			RoomsMutex:    &sync.Mutex{},
			ClientTimeout: 30 * time.Second,
			PingInterval:  0,
			cfg:           cfg,
			RedisClient:   rc,
			redisCtx:      ctxSrv,
			redisCancel:   cancel,
		}
		srv.initRooms()

		// Hydrate channels from Redis (simulates restart).
		hyCtx, hyCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := srv.hydrateChannelsFromRedis(hyCtx); err != nil {
			t.Fatalf("hydrate: %v", err)
		}
		hyCancel()

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

		return ln.Addr().String()
	}

	addr := makeServer()
	return addr, makeServer
}

// TestChannelTopicPersistence verifies that a channel topic survives a server restart.
// Workflow: start server, JOIN #foo, set TOPIC, "restart" (new Server same Redis), JOIN #foo, topic is preserved.
func TestChannelTopicPersistence(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	addr1, makeServer := startPersistenceServer(t, mr)

	// Phase 1: connect, join #foo, set topic.
	c1 := dialServer(t, addr1)
	register(t, c1, "persistuser")

	c1.send(t, "JOIN #foo")
	c1.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "foo") })

	c1.send(t, "TOPIC #foo :Persistence topic")
	c1.readUntil(t, func(l string) bool { return strings.Contains(l, "TOPIC") && strings.Contains(l, "Persistence topic") })

	// Close the connection (simulate restart — the server state stays in Redis).
	// Start a new server connected to the same miniredis.
	addr2 := makeServer()
	time.Sleep(50 * time.Millisecond) // give server time to hydrate

	// Phase 2: connect to new server, join #foo, verify topic is preserved.
	c2 := dialServer(t, addr2)
	register(t, c2, "persistuser2")

	c2.send(t, "JOIN #foo")

	// Wait for 332 RPL_TOPIC for #foo specifically (topic preserved).
	// Skip 331 messages for other channels (like lobby).
	var topicLine string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c2.conn.SetReadDeadline(deadline)
		line, err := c2.reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		// We want a 332 message specifically mentioning #foo.
		if (strings.Contains(line, " 332 ") || strings.Contains(line, " 331 ")) && strings.Contains(line, "foo") {
			topicLine = line
			break
		}
	}

	if !strings.Contains(topicLine, " 332 ") {
		t.Errorf("expected 332 RPL_TOPIC for #foo (topic preserved), got: %q", topicLine)
	}
	if !strings.Contains(topicLine, "Persistence topic") {
		t.Errorf("expected topic 'Persistence topic', got: %q", topicLine)
	}
}
