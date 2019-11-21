package server

import (
	"sync"

	"github.com/google/uuid"
)

// Room struct holding info about created a room.
type Room struct {
	identifier uuid.UUID
	name       string
	topic      string
	clients    map[uuid.UUID]bool
	roomMux    *sync.Mutex
}
