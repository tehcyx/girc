// Package server provides IRC server implementation
package server

import (
	"sync"

	"github.com/google/uuid"
)

// Room represents an IRC channel (chat room).
// Rooms can contain multiple clients and have properties like name and topic.
// All room operations should be protected by the roomMux mutex for thread safety.
type Room struct {
	identifier uuid.UUID
	name       string
	topic      string
	clients    map[uuid.UUID]bool
	roomMux    *sync.Mutex

	// Channel mode flags
	topicLock    bool               // +t - only operators can set topic
	noExternal   bool               // +n - no external messages
	moderated    bool               // +m - moderated channel
	inviteOnly   bool               // +i - invite only
	secret       bool               // +s - secret channel
	private      bool               // +p - private channel
	operators    map[uuid.UUID]bool // Channel operators
	voiced       map[uuid.UUID]bool // Voiced users (can speak in moderated channel)
	founders     map[uuid.UUID]bool // Channel founders (highest privilege)
	registered   bool               // Whether channel is registered
}
