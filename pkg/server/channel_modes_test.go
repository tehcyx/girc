package server

import (
	"testing"

	"github.com/tehcyx/girc/internal/config"
)

// TestDefaultChannelModesApplied verifies that createRoom applies the
// DefaultChannelModes from config.Values to the new room.
//
// NOTE: createRoom reads from config.Values (global) rather than the server's
// s.cfg. This is a known code-writer feedback item: createRoom should accept
// a config parameter or read from s.cfg so tests don't need to touch globals.
func TestDefaultChannelModesApplied(t *testing.T) {
	prev := config.Values
	defer func() { config.Values = prev }()
	cfg := &config.Config{}
	cfg.Server.DefaultChannelModes = "+m"
	config.Values = cfg

	room := createRoom("testmodechan")

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
	prev := config.Values
	defer func() { config.Values = prev }()
	cfg := &config.Config{}
	cfg.Server.DefaultChannelModes = "+nt"
	config.Values = cfg

	room := createRoom("testntchan")

	if !room.topicLock {
		t.Error("expected room.topicLock=true for '+nt'")
	}
	if !room.noExternal {
		t.Error("expected room.noExternal=true for '+nt'")
	}
}

// TestDefaultChannelModesFallback verifies that when config.Values is nil
// createRoom falls back to the hardcoded "+nt" default.
func TestDefaultChannelModesFallback(t *testing.T) {
	prev := config.Values
	defer func() { config.Values = prev }()
	config.Values = nil

	room := createRoom("testfallbackchan")

	// Hardcoded fallback is "+nt".
	if !room.topicLock {
		t.Error("expected topicLock=true from hardcoded fallback '+nt'")
	}
	if !room.noExternal {
		t.Error("expected noExternal=true from hardcoded fallback '+nt'")
	}
}
