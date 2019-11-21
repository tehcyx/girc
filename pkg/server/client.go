package server

import (
	"net"
	"sync"

	"github.com/google/uuid"
)

// Client struct holding info about connected client.
type Client struct {
	identifier uuid.UUID
	nick       string
	user       string
	mode       int
	unused     string
	realname   string
	rooms      map[uuid.UUID]bool
	thread     chan int
	conn       net.Conn
	clientMux  *sync.Mutex
}
