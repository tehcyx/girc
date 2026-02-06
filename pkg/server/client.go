// Package server provides IRC server implementation
package server

import (
	"net"
	"sync"

	"github.com/google/uuid"
)

// Client represents a connected IRC client.
// It stores the client's identity, connection info, and room memberships.
// All client operations should be protected by the clientMux mutex for thread safety.
type Client struct {
	identifier uuid.UUID
	nick       string
	user       string
	mode       int    // User registration mode parameter
	unused     string
	realname   string
	rooms      map[uuid.UUID]bool
	thread     chan int
	conn       net.Conn
	clientMux  *sync.Mutex

	// User mode flags
	invisible bool // +i - invisible mode
	wallops   bool // +w - receive wallops
	notices   bool // +s - receive server notices
	operator  bool // +o - IRC operator
	away      bool   // Away status (for WHO command)
	awayMessage string // Away message text

	// Authentication state
	authenticated bool   // Whether user has authenticated with password
	accountName   string // Registered account name (if authenticated)
	password      string // Password provided via PASS command (cleared after use)
}
