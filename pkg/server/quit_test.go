package server

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestQUITNotifiesChannelMembers verifies that when a client QUITs, members
// of all shared channels receive a QUIT notification.
func TestQUITNotifiesChannelMembers(t *testing.T) {
	addr := startTestServer(t)

	quitter := dialServer(t, addr)
	observer := dialServer(t, addr)

	register(t, quitter, "quitter")
	time.Sleep(50 * time.Millisecond)
	register(t, observer, "observer")
	time.Sleep(50 * time.Millisecond)

	// Both join the same channel so they share it.
	quitter.send(t, "JOIN #quitchan")
	quitter.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "quitchan") })
	observer.send(t, "JOIN #quitchan")
	observer.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "quitchan") })

	// quitter sends QUIT.
	quitter.send(t, "QUIT :goodbye")

	// observer should receive QUIT notification.
	line := observer.readUntil(t, func(l string) bool {
		return strings.Contains(l, "QUIT")
	})
	if !strings.Contains(line, "quitter") {
		t.Errorf("expected QUIT notification mentioning 'quitter', got: %s", line)
	}
}

// TestQUITWithoutReason verifies that QUIT without a reason message still
// notifies channel members.
func TestQUITWithoutReason(t *testing.T) {
	addr := startTestServer(t)

	quitter := dialServer(t, addr)
	observer := dialServer(t, addr)

	register(t, quitter, "quitter2")
	time.Sleep(50 * time.Millisecond)
	register(t, observer, "observer2")
	time.Sleep(50 * time.Millisecond)

	quitter.send(t, "JOIN #quitchan2")
	quitter.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "quitchan2") })
	observer.send(t, "JOIN #quitchan2")
	observer.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "quitchan2") })

	quitter.send(t, "QUIT")

	line := observer.readUntil(t, func(l string) bool {
		return strings.Contains(l, "QUIT") || strings.Contains(l, "ERROR")
	})
	// We just need some notification; the format may vary.
	if line == "" {
		t.Error("expected QUIT or ERROR notification after peer QUIT, got nothing")
	}
}

// TestRemoveClientDoesNotDeleteIndexZero verifies that RemoveClient with an
// unknown UUID does not corrupt the client list by deleting index 0.
func TestRemoveClientDoesNotDeleteIndexZero(t *testing.T) {
	s := &Server{
		Clients:      []Client{},
		ClientsMutex: &sync.Mutex{},
		Rooms:        []Room{},
		RoomsMutex:   &sync.Mutex{},
		cfg:          testConfig(),
	}

	// Add two legitimate clients with known identifiers.
	id1 := uuid.Must(uuid.NewRandom())
	id2 := uuid.Must(uuid.NewRandom())

	s.ClientsMutex.Lock()
	s.Clients = append(s.Clients, Client{identifier: id1, nick: "first"})
	s.Clients = append(s.Clients, Client{identifier: id2, nick: "second"})
	s.ClientsMutex.Unlock()

	// Remove a non-existent ID — should be a no-op.
	nonExistent := uuid.Must(uuid.NewRandom())
	s.RemoveClient(nonExistent)

	s.ClientsMutex.Lock()
	count := len(s.Clients)
	nick0 := s.Clients[0].nick
	s.ClientsMutex.Unlock()

	if count != 2 {
		t.Errorf("expected 2 clients after removing unknown ID, got %d", count)
	}
	if nick0 != "first" {
		t.Errorf("index 0 client was changed; expected 'first', got %q", nick0)
	}
}
