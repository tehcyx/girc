package server

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tehcyx/girc/internal/config"
)

// startServerWithHost starts a test server using the supplied hostname.
func startServerWithHost(t *testing.T, host string) string {
	t.Helper()

	cfg := &config.Config{}
	cfg.Server.Name = "testserver"
	cfg.Server.Host = host
	cfg.Server.Port = "0"
	cfg.Server.Motd = "Test MOTD"
	cfg.Server.Debug = false
	cfg.Server.DefaultChannelModes = "+nt"

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := &Server{
		Clients:       []Client{},
		ClientsMutex:  &sync.Mutex{},
		Rooms:         []Room{},
		RoomsMutex:    &sync.Mutex{},
		ClientTimeout: 30 * time.Second,
		PingInterval:  0,
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

// TestMODEBroadcastUsesConfigHost verifies that a MODE broadcast uses the
// server's configured hostname (s.cfg.Server.Host) rather than a hardcoded
// "localhost" (regression for the MODE host fix in iter 2).
func TestMODEBroadcastUsesConfigHost(t *testing.T) {
	const customHost = "irc.example.test"
	addr := startServerWithHost(t, customHost)

	op := dialServer(t, addr)
	register(t, op, "modeop")

	op.send(t, "JOIN #hosttest")
	op.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "hosttest") })

	// op is the channel founder/op; set mode +m.
	op.send(t, "MODE #hosttest +m")

	// readUntil uses the ircConn's buffered reader, not a fresh one.
	modeLine := op.readUntil(t, func(l string) bool {
		return strings.Contains(l, "MODE") && strings.Contains(l, "hosttest")
	})

	if !strings.Contains(modeLine, customHost) {
		t.Errorf("MODE broadcast prefix should contain %q, got: %s", customHost, modeLine)
	}
	// A1 regression: the channel name in a MODE broadcast must carry the '#' prefix.
	if !strings.Contains(modeLine, "#hosttest") {
		t.Errorf("MODE broadcast must contain '#hosttest' (with # prefix), got: %s", modeLine)
	}
}

// TestDefaultChannelModesAppliedOnJoin is an integration test: it boots a
// server with DefaultChannelModes="+m" and confirms that a newly joined channel
// reports +m when queried via MODE.
func TestDefaultChannelModesAppliedOnJoin(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.Name = "testserver"
	cfg.Server.Host = "localhost"
	cfg.Server.Port = "0"
	cfg.Server.Motd = "Test"
	cfg.Server.DefaultChannelModes = "+m"

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := &Server{
		Clients:       []Client{},
		ClientsMutex:  &sync.Mutex{},
		Rooms:         []Room{},
		RoomsMutex:    &sync.Mutex{},
		ClientTimeout: 30 * time.Second,
		PingInterval:  0,
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
	addr := ln.Addr().String()

	c := dialServer(t, addr)
	register(t, c, "defaultmodeuser")

	c.send(t, "JOIN #defaultmodechan")
	c.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "defaultmodechan") })

	c.send(t, "MODE #defaultmodechan")

	modeLine := c.readUntil(t, func(l string) bool {
		return (strings.Contains(l, "324") || strings.Contains(l, "MODE")) &&
			strings.Contains(l, "defaultmodechan")
	})

	if !strings.Contains(modeLine, "m") {
		t.Errorf("expected +m in channel modes (DefaultChannelModes=+m), got: %s", modeLine)
	}

	// Also assert via the unit path that createRoom applies +m.
	room := srv.createRoom("checkroom")
	if !room.moderated {
		t.Error("createRoom: expected moderated=true for DefaultChannelModes=+m")
	}
}
