package server

import (
	"sync"
	"testing"

	"github.com/tehcyx/girc/internal/config"
)

// newServerWithCfg returns a minimal server with the given config for unit tests.
func newServerWithCfg(cfg *config.Config) *Server {
	return &Server{
		Clients:      []Client{},
		ClientsMutex: &sync.Mutex{},
		Rooms:        []Room{},
		RoomsMutex:   &sync.Mutex{},
		cfg:          cfg,
	}
}

// TestDefaultChannelModesApplied verifies that createRoom applies the
// DefaultChannelModes from s.cfg to the new room.
func TestDefaultChannelModesApplied(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.DefaultChannelModes = "+m"
	s := newServerWithCfg(cfg)

	room := s.createRoom("testmodechan")

	if !room.moderated {
		t.Error("expected room.moderated=true for default mode '+m', got false")
	}
	if room.noExternal {
		t.Error("expected room.noExternal=false when default is '+m' (no +n), got true")
	}
	if room.topicLock {
		t.Error("expected room.topicLock=false when default is '+m' (no +t), got true")
	}
}

// TestDefaultChannelModesNT verifies that the standard default "+nt" sets
// topicLock and noExternal on newly created rooms.
func TestDefaultChannelModesNT(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.DefaultChannelModes = "+nt"
	s := newServerWithCfg(cfg)

	room := s.createRoom("testntchan")

	if !room.topicLock {
		t.Error("expected room.topicLock=true for '+nt'")
	}
	if !room.noExternal {
		t.Error("expected room.noExternal=true for '+nt'")
	}
}

// TestDefaultChannelModesFallback verifies that when s.cfg is nil
// createRoom falls back to the global config.Values, and when that is nil
// falls back to the hardcoded "+nt" default.
func TestDefaultChannelModesFallback(t *testing.T) {
	prev := config.Values
	defer func() { config.Values = prev }()
	config.Values = nil

	s := newServerWithCfg(nil)
	room := s.createRoom("testfallbackchan")

	// Hardcoded fallback is "+nt".
	if !room.topicLock {
		t.Error("expected topicLock=true from hardcoded fallback '+nt'")
	}
	if !room.noExternal {
		t.Error("expected noExternal=true from hardcoded fallback '+nt'")
	}
}
