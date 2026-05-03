package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/tehcyx/girc/internal/config"
	"github.com/tehcyx/girc/pkg/redis"
	"github.com/tehcyx/girc/pkg/version"
)

type Server struct {
	Clients      []Client
	ClientsMutex *sync.Mutex

	Rooms      []Room
	RoomsMutex *sync.Mutex

	Lobby *Room

	ClientTimeout time.Duration

	// PingInterval controls how often the server pings each client.
	// 0 means no pings (useful in tests).
	PingInterval time.Duration

	// cfg holds the server's active configuration. If nil, falls back to
	// the global config.Values.
	cfg *config.Config

	// Redis client for distributed state management (nil if disabled)
	RedisClient *redis.Client

	// Context for cancelling Redis event subscription
	redisCtx    context.Context
	redisCancel context.CancelFunc
}

//New creates a new server, listening on the defined port and starting to accept client connections
func New() {
	log.Println("Launching server...")

	cfg := config.Values

	// listen on all interfaces
	ln, err := net.Listen("tcp", fmt.Sprintf(":%s", cfg.Server.Port))
	if err != nil {
		log.Error(fmt.Errorf("listen failed, port possibly in use already: %v", err))
	}

	defer func() {
		log.Printf("Shutting down server. Bye!\n")

		// Cleanup will be handled after srv is initialized
		ln.Close()
	}()

	srv := &Server{
		Clients:      []Client{},
		ClientsMutex: &sync.Mutex{},

		Rooms:      []Room{},
		RoomsMutex: &sync.Mutex{},

		ClientTimeout: 15 * time.Minute,
		PingInterval:  90 * time.Second,
		cfg:           cfg,
	}

	// Initialize Redis client if enabled
	if cfg.Redis.Enabled {
		log.Println("Redis enabled, initializing distributed state management...")

		// Generate pod ID if not provided
		podID := cfg.Redis.PodID
		if podID == "" {
			podID = uuid.Must(uuid.NewRandom()).String()
			log.Printf("Generated pod ID: %s", podID)
		}

		redisClient, err := redis.NewClient(cfg.Redis.URL, podID)
		if err != nil {
			log.Errorf("Failed to initialize Redis client: %v", err)
			log.Println("Continuing in non-distributed mode...")
		} else {
			srv.RedisClient = redisClient
			log.Println("Redis client initialized successfully")

			// Create context for Redis event subscription
			srv.redisCtx, srv.redisCancel = context.WithCancel(context.Background())

			// Register pod with Redis
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := srv.RedisClient.RegisterPod(ctx, version.GetVersion()); err != nil {
				log.Errorf("Failed to register pod: %v", err)
			} else {
				log.Infof("Pod registered in Redis")
			}
			cancel()

			// Start background goroutines for pod coordination
			go srv.subscribeToRedisEvents()
			go srv.startHeartbeat()
			go srv.startOrphanCleanup()
		}
	} else {
		log.Println("Redis disabled, running in single-server mode")
	}

	srv.initRooms()

	// Hydrate channels from Redis if enabled.
	if srv.RedisClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := srv.hydrateChannelsFromRedis(ctx); err != nil {
			log.Errorf("Failed to hydrate channels from Redis: %v", err)
		}
		cancel()
	}

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start accepting connections in a goroutine
	go func() {
		for {
			// accept connection on port
			conn, err := ln.Accept()
			if err != nil {
				// Check if we're shutting down
				select {
				case <-sigChan:
					return
				default:
					log.Errorf("Failed to accept connection: %v", err)
					continue
				}
			}
			go srv.handleClientConnect(conn)
		}
	}()

	// Wait for shutdown signal
	<-sigChan
	log.Println("Received shutdown signal, starting graceful shutdown...")

	// Disconnect all clients gracefully with QUIT message
	srv.ClientsMutex.Lock()
	clients := make([]Client, len(srv.Clients))
	copy(clients, srv.Clients)
	srv.ClientsMutex.Unlock()

	log.Printf("Disconnecting %d clients gracefully...", len(clients))
	for _, client := range clients {
		if client.conn != nil {
			// Send QUIT message to client
			send(client.conn, "ERROR :Server shutting down\r\n")
			client.conn.Close()
		}
	}

	// Cleanup Redis state if enabled
	if srv.RedisClient != nil {
		// Cancel Redis event subscription context
		if srv.redisCancel != nil {
			srv.redisCancel()
			log.Println("Cancelled Redis event subscription...")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		log.Println("Cleaning up Redis state...")
		if err := srv.RedisClient.GracefulShutdown(ctx); err != nil {
			log.Errorf("Failed to cleanup Redis state: %v", err)
		}

		if err := srv.RedisClient.Close(); err != nil {
			log.Errorf("Failed to close Redis connection: %v", err)
		}
	}

	log.Println("Graceful shutdown complete")
}

func (s *Server) getConfig() *config.Config {
	if s.cfg == nil {
		return config.Values
	}
	return s.cfg
}

func (s *Server) initRooms() {
	lobby := s.createRoom("lobby")

	s.RoomsMutex.Lock()
	defer s.RoomsMutex.Unlock()

	s.Rooms = append(s.Rooms, lobby)
	s.Lobby = &lobby
}

func (s *Server) handleClientConnect(conn net.Conn) {
	log.Printf("Client connecting, handling connection ...\n")
	identifier := uuid.Must(uuid.NewRandom())
	if s.getConfig().Server.Debug {
		log.Debugf("This is concurrent user #%d, generated UUID is: %s\n", len(s.Clients), identifier)
	}

	client := Client{}
	client.clientMux = &sync.Mutex{}
	client.identifier = identifier
	client.conn = conn
	client.rooms = make(map[uuid.UUID]bool)

	defer func() {
		// Determine disconnect reason
		var disconnectReason string
		_, err := conn.Read(make([]byte, 0))
		if err != io.EOF {
			// Connection error or timeout
			disconnectReason = "Connection lost"
			log.Printf("Client hung up unexpectedly or quit ...\n")
		} else {
			// Clean disconnect
			disconnectReason = "Client disconnected"
			log.Printf("Client quit or inactive, closing connection ...\n")
		}

		// Send ERROR message to the client to notify them of the disconnect
		// Format: ERROR :Closing Link: <reason>
		if client.nick != "" {
			errorMsg := fmt.Sprintf("ERROR :Closing Link: %s (%s)\r\n", client.nick, disconnectReason)
			conn.Write([]byte(errorMsg))
			if s.getConfig().Server.Debug {
		log.Debugf("Sent ERROR to %s: %s", client.nick, disconnectReason)
	}
		}

		// Send QUIT notifications to all users in the same channels
		// This lets other users know this client has left
		if client.nick != "" {
			quitMsg := fmt.Sprintf(":%s!%s@%s QUIT :%s\r\n",
				client.nick, client.user, s.getConfig().Server.Host, disconnectReason)

			// Collect all unique clients in the same rooms
			notifiedClients := make(map[uuid.UUID]bool)

			for roomID := range client.rooms {
				s.RoomsMutex.Lock()
				var room *Room
				for i := range s.Rooms {
					if s.Rooms[i].identifier == roomID {
						room = &s.Rooms[i]
						break
					}
				}
				s.RoomsMutex.Unlock()

				if room != nil {
					room.roomMux.Lock()
					// room.clients is map[uuid.UUID]bool, need to look up actual clients
					for clientID := range room.clients {
						// Don't send to the disconnecting client or duplicates
						if clientID != client.identifier && !notifiedClients[clientID] {
							// Look up the actual client from the server
							s.ClientsMutex.Lock()
							for i := range s.Clients {
								if s.Clients[i].identifier == clientID {
									if s.Clients[i].conn != nil {
										send(s.Clients[i].conn, "%s", quitMsg)
										notifiedClients[clientID] = true
									}
									break
								}
							}
							s.ClientsMutex.Unlock()
						}
					}
					room.roomMux.Unlock()
				}
			}

			if len(notifiedClients) > 0 {
				if s.getConfig().Server.Debug {
		log.Debugf("Sent QUIT notification for %s to %d users", client.nick, len(notifiedClients))
	}
			}
		}

		// Remove client from server state and close connection
		s.RemoveClient(client.identifier)
		conn.Close()
	}()

	// Create reader ONCE before the loop, not on every iteration
	// This prevents buffered data from being lost between reads
	reader := bufio.NewReader(conn)

	for {
		// Set a deadline for reading. Read operation will fail if no data
		// is received after deadline.
		conn.SetReadDeadline(time.Now().Add(s.ClientTimeout))

		// TODO https://twitter.com/davecheney/status/604837853344989184?lang=en
		message, err := reader.ReadString('\n') // there's a problem with clients that send multiple lines, will have to figure out a solution using the scanner API

		// scanner := bufio.NewScanner(conn)
		// var message string
		// for scanner.Scan() {
		// 	message += scanner.Text()
		// }
		// if err := scanner.Err(); err != nil {
		// 	log.Error(fmt.Errorf("reading standard input: %w", err))
		// }

		if err != nil {
			log.Errorf("An error occured, now closing connection to client socket. Error: %s\n", err)
			return
		} else if len(message) == 0 {
			log.Infof("Received empty message, continuing...")
			continue
		} else {
			if s.getConfig().Server.Debug {
				log.Infof("Received message from client: %s", message)
			}
			message = strings.TrimSpace(message)
			tokens := strings.Split(message, " ")

			cmd := strings.ToUpper(tokens[0]) // Make command case-insensitive
			args := strings.Join(tokens[1:], " ")

			// Log command parsing, but sanitize sensitive commands (PASS, REGISTER, SETPASS)
			if s.getConfig().Server.Debug {
				if cmd == PassCmd || cmd == RegisterCmd || cmd == SetPassCmd {
					log.Infof("Parsed command: '%s', args: [REDACTED]", cmd)
				} else {
					log.Infof("Parsed command: '%s', args: '%s'", cmd, args)
				}
			}

			if !s.handleCommand(&client, conn, cmd, args) {
				return
			}
		}
	}
}

func (s *Server) AddClient(client Client) error {
	log.Infof("AddClient: Adding client %s (UUID: %s)", client.nick, client.identifier)
	s.ClientsMutex.Lock()
	s.Clients = append(s.Clients, client)
	log.Infof("AddClient: Client added, total clients now: %d", len(s.Clients))
	s.ClientsMutex.Unlock()

	// Register client in Redis if enabled
	if s.RedisClient != nil {
		ctx := context.Background()
		clientData := redis.ClientData{
			UUID:     client.identifier.String(),
			Nick:     client.nick,
			User:     client.user,
			Host:     s.getConfig().Server.Host,
			Channels: []string{}, // Will be updated when joining channels
		}
		if err := s.RedisClient.RegisterClient(ctx, clientData); err != nil {
			log.Errorf("Failed to register client in Redis: %v", err)
			// Continue anyway - client is registered locally
		} else {
			if s.getConfig().Server.Debug {
		log.Debugf("Registered client %s in Redis", client.nick)
	}
		}
	}

	send(client.conn, ":%s %s %s :Welcome to the Internet Relay Network %s!%s@%s\n", s.getConfig().Server.Host, RplWelcome, client.nick, client.nick, client.user, s.getConfig().Server.Host)
	send(client.conn, ":%s %s %s :Your host is %s, running version %s\n", s.getConfig().Server.Host, RplYourHost, client.nick, s.getConfig().Server.Host, version.GetVersion())
	send(client.conn, ":%s %s %s :This server was created %s\n", s.getConfig().Server.Host, RplCreated, client.nick, time.Now().String())

	// 004 RPL_MYINFO
	send(client.conn, ":%s %s %s %s %s oiwsz biklmnopstv\n",
		s.getConfig().Server.Host, RplMyInfo, client.nick,
		s.getConfig().Server.Host, version.GetVersion())

	// 005 RPL_ISUPPORT (single line, commonly expected by clients)
	send(client.conn, ":%s %s %s CHANTYPES=# PREFIX=(ov)@+ NETWORK=%s CASEMAPPING=ascii :are supported by this server\n",
		s.getConfig().Server.Host, RplBounce, client.nick, s.getConfig().Server.Name)

	// MOTD block: 375 / 372* / 376
	serverName := s.getConfig().Server.Host
	send(client.conn, ":%s %s %s :- %s Message of the day -\n", serverName, RplMotdStart, client.nick, serverName)
	motd := s.getConfig().Server.Motd
	if motd == "" {
		motd = "Welcome to girc"
	}
	for _, line := range strings.Split(motd, "\n") {
		send(client.conn, ":%s %s %s :- %s\n", serverName, RplMotd, client.nick, line)
	}
	send(client.conn, ":%s %s %s :End of MOTD command\n", serverName, RplEndOfMotd, client.nick)

	return s.JoinRoomByName(client, s.Lobby.name)
}

func (s *Server) ClientExists(nick string) bool {
	s.ClientsMutex.Lock()
	defer s.ClientsMutex.Unlock()

	for _, client := range s.Clients {
		if strings.Compare(client.nick, nick) == 0 {
			return true
		}
	}
	return false
}

func (s *Server) ClientByName(nick string) Client {
	s.ClientsMutex.Lock()
	defer s.ClientsMutex.Unlock()

	for _, client := range s.Clients {
		if strings.Compare(client.nick, nick) == 0 {
			return client
		}
	}
	return Client{}
}

func (s *Server) JoinRoomByName(client Client, roomName string) error {
	if len(roomName) == 0 {
		return fmt.Errorf("Room name is invalid \"%s\"", roomName)
	}
	if strings.HasPrefix(roomName, "#") {
		roomName = roomName[1:]
	}

	// Find the actual client in the server's Clients slice (not the parameter copy)
	var clientPtr *Client
	s.ClientsMutex.Lock()
	log.Infof("JoinRoomByName: Looking for client %s (UUID: %s) in %d clients", client.nick, client.identifier, len(s.Clients))
	for i := range s.Clients {
		if s.Clients[i].identifier == client.identifier {
			clientPtr = &s.Clients[i]
			log.Infof("JoinRoomByName: Found client %s at index %d", client.nick, i)
			break
		}
	}
	s.ClientsMutex.Unlock()

	if clientPtr == nil {
		log.Errorf("JoinRoomByName: Client %s (UUID: %s) not found in server", client.nick, client.identifier)
		return fmt.Errorf("Client not found in server")
	}

	var roomPtr *Room
	s.RoomsMutex.Lock()
	for i := range s.Rooms {
		if strings.Compare(s.Rooms[i].name, roomName) == 0 {
			roomPtr = &s.Rooms[i]
			break
		}
	}
	isNewRoom := false
	if roomPtr == nil {
		newRoom := s.createRoom(roomName)
		s.Rooms = append(s.Rooms, newRoom)
		roomPtr = &s.Rooms[len(s.Rooms)-1]
		isNewRoom = true
	}
	s.RoomsMutex.Unlock()

	roomPtr.roomMux.Lock()
	roomPtr.clients[clientPtr.identifier] = true
	// If this is a new room, make the first user an operator
	if isNewRoom {
		roomPtr.operators[clientPtr.identifier] = true
		log.Infof("JoinRoomByName: Made %s an operator of new room #%s", clientPtr.nick, roomName)
	}
	roomPtr.roomMux.Unlock()

	clientPtr.clientMux.Lock()
	clientPtr.rooms[roomPtr.identifier] = true
	clientPtr.clientMux.Unlock()

	// Update Redis if enabled
	if s.RedisClient != nil {
		ctx := context.Background()
		channelName := "#" + roomName

		// Join channel in Redis
		if err := s.RedisClient.JoinChannel(ctx, clientPtr.identifier.String(), channelName); err != nil {
			log.Errorf("Failed to join channel in Redis: %v", err)
			// Continue anyway - channel is joined locally
		} else {
			if s.getConfig().Server.Debug {
		log.Debugf("Client %s joined channel %s in Redis", clientPtr.nick, channelName)
	}
		}

		// Publish JOIN event to other pods
		event := redis.Event{
			Type: "JOIN",
			Data: map[string]interface{}{
				"uuid":    clientPtr.identifier.String(),
				"nick":    clientPtr.nick,
				"user":    clientPtr.user,
				"channel": channelName,
				"pod_id":  s.RedisClient.PodID(),
			},
		}
		if err := s.RedisClient.PublishEvent(ctx, event); err != nil {
			log.Errorf("Failed to publish JOIN event: %v", err)
			// Continue anyway - local clients will be notified
		}
	}

	// send to all existing clients + user, that user has joined:
	// Take snapshot of room clients while holding lock
	roomPtr.roomMux.Lock()
	clientIDs := make([]uuid.UUID, 0, len(roomPtr.clients))
	for ident := range roomPtr.clients {
		clientIDs = append(clientIDs, ident)
	}
	roomPtr.roomMux.Unlock()

	var names []string
	s.ClientsMutex.Lock()
	for _, ident := range clientIDs {
		for i := range s.Clients {
			if ident == s.Clients[i].identifier {
				send(s.Clients[i].conn, ":%s!%s@%s %s #%s\n", clientPtr.nick, clientPtr.user, s.getConfig().Server.Host, JoinCmd, roomPtr.name)
				names = append(names, s.Clients[i].nick)
				break
			}
		}
	}
	s.ClientsMutex.Unlock()

	if roomPtr.topic == "" {
		// 331 RPL_NOTOPIC
		send(clientPtr.conn, ":%s %s %s #%s :No topic is set\n", s.getConfig().Server.Host, RplNoTopic, clientPtr.nick, roomPtr.name)
	} else {
		// 332 RPL_TOPIC
		send(clientPtr.conn, ":%s %s %s #%s :%s\n", s.getConfig().Server.Host, RplTopic, clientPtr.nick, roomPtr.name, roomPtr.topic)
	}

	// send list of all clients in room to user
	// "( "=" / "*" / "@" ) <channel> :[ "@" / "+" ] <nick> *( " " [ "@" / "+" ] <nick> )
	send(clientPtr.conn, ":%s %s %s = #%s :%s\n", s.getConfig().Server.Host, RplNameReply, clientPtr.nick, roomPtr.name, strings.Join(names[:], " "))

	send(clientPtr.conn, ":%s %s %s #%s :End of NAMES list\n", s.getConfig().Server.Host, RplEndOfNames, clientPtr.nick, roomPtr.name)
	return nil
}

func (s *Server) RemoveClient(id uuid.UUID) {
	// Unregister from Redis if enabled
	if s.RedisClient != nil {
		ctx := context.Background()
		if err := s.RedisClient.UnregisterClient(ctx, id.String()); err != nil {
			log.Errorf("Failed to unregister client from Redis: %v", err)
			// Continue anyway - will remove from local state
		} else {
			if s.getConfig().Server.Debug {
		log.Debugf("Unregistered client %s from Redis", id.String())
	}
		}
	}

	s.ClientsMutex.Lock()
	defer s.ClientsMutex.Unlock()

	index := -1
	for num, iter := range s.Clients {
		if iter.identifier == id {
			index = num
			break
		}
	}
	if index == -1 {
		return // not found; nothing to do
	}
	s.Clients = append(s.Clients[:index], s.Clients[index+1:]...)
}

func (s *Server) IsUserInRoom(client Client, roomName string) (bool, *Room) {
	compareRoomName := roomName
	if strings.HasPrefix(roomName, "#") {
		compareRoomName = roomName[1:]
	}
	s.RoomsMutex.Lock()
	defer s.RoomsMutex.Unlock()

	for i := range s.Rooms {
		if strings.Compare(s.Rooms[i].name, compareRoomName) == 0 {
			if _, ok := client.rooms[s.Rooms[i].identifier]; ok {
				return true, &s.Rooms[i]
			}
		}
	}
	return false, nil
}

func (s *Server) RoomExists(roomName string) bool {
	s.RoomsMutex.Lock()
	defer s.RoomsMutex.Unlock()

	if strings.HasPrefix(roomName, "#") {
		roomName = roomName[1:]
	}

	for _, room := range s.Rooms {
		if strings.Compare(room.name, roomName) == 0 {
			return true
		}
	}
	return false
}

// GetRoom retrieves a room by name, returns nil if not found
func (s *Server) GetRoom(roomName string) *Room {
	s.RoomsMutex.Lock()
	defer s.RoomsMutex.Unlock()

	if strings.HasPrefix(roomName, "#") {
		roomName = roomName[1:]
	}

	for i := range s.Rooms {
		if s.Rooms[i].name == roomName {
			return &s.Rooms[i]
		}
	}
	return nil
}

// UpdateClientNick updates a client's nickname
func (s *Server) UpdateClientNick(clientID uuid.UUID, newNick string) {
	s.ClientsMutex.Lock()
	defer s.ClientsMutex.Unlock()

	for i := range s.Clients {
		if s.Clients[i].identifier == clientID {
			s.Clients[i].nick = newNick
			return
		}
	}
}

func (s *Server) createRoom(name string) Room {
	r := Room{
		roomMux:    &sync.Mutex{},
		identifier: uuid.Must(uuid.NewRandom()),
		name:       name,
		clients:    make(map[uuid.UUID]bool),
		topic:      "",
		operators:  make(map[uuid.UUID]bool),
		voiced:     make(map[uuid.UUID]bool),
		founders:   make(map[uuid.UUID]bool),
		registered: false,
	}
	// Apply default channel modes from server config (e.g. "+nt").
	modes := "+nt"
	if cfg := s.getConfig(); cfg != nil && cfg.Server.DefaultChannelModes != "" {
		modes = cfg.Server.DefaultChannelModes
	}
	applyModeString(&r, modes)
	return r
}

// roomModeString returns a mode string representing the current flags on a room (e.g. "+nt").
func roomModeString(r *Room) string {
	var b strings.Builder
	b.WriteString("+")
	if r.topicLock {
		b.WriteString("t")
	}
	if r.noExternal {
		b.WriteString("n")
	}
	if r.moderated {
		b.WriteString("m")
	}
	if r.inviteOnly {
		b.WriteString("i")
	}
	if r.secret {
		b.WriteString("s")
	}
	if r.private {
		b.WriteString("p")
	}
	return b.String()
}

// applyModeString applies a mode string like "+nt" to a room.
func applyModeString(r *Room, modes string) {
	adding := true
	for _, ch := range modes {
		switch ch {
		case '+':
			adding = true
		case '-':
			adding = false
		case 't':
			r.topicLock = adding
		case 'n':
			r.noExternal = adding
		case 'm':
			r.moderated = adding
		case 'i':
			r.inviteOnly = adding
		case 's':
			r.secret = adding
		case 'p':
			r.private = adding
		}
	}
}

func validNickCharset(test string) bool {
	re, err := regexp.Compile("^[-#&*_a-zA-Z0-9]{1,}$")
	if err != nil {
		log.Errorf("Something went wrong validating the nickname")
		return false
	}
	if !re.MatchString(test) {
		return false
	}
	return true
}

func validMessageMask(mask string) bool {
	return false
}

func send(conn net.Conn, format string, a ...interface{}) {
	message := fmt.Sprintf(format, a...)
	// Normalise line endings: strip any trailing \n, always append \r\n.
	message = strings.TrimRight(message, "\n")
	message = strings.TrimRight(message, "\r")
	message += "\r\n"
	log.Debugf(">> %s", strings.TrimRight(message, "\r\n"))
	conn.Write([]byte(message))
}

// func clientReadConnectPass(args string) string {
// 	// char* client_read_connect_pass(char* cmd, char* args, bool* quit) {
// 	// 	// hello

// 	// 	// 461 %s %s :Not enough parameters

// 	// 	// 464 :Password incorrect
// 	// 	return NULL;
// 	// }
// 	return ""
// }

// hydrateChannelsFromRedis loads persisted channel state (topic, modes) from Redis
// and creates the corresponding rooms on this server.
func (s *Server) hydrateChannelsFromRedis(ctx context.Context) error {
	if s.RedisClient == nil {
		return nil
	}
	channels, err := s.RedisClient.HydrateChannels(ctx)
	if err != nil {
		return err
	}

	for _, ch := range channels {
		channelName := ch.Name
		if strings.HasPrefix(channelName, "#") {
			channelName = channelName[1:]
		}
		if channelName == "lobby" {
			continue // lobby is always created by initRooms
		}

		s.RoomsMutex.Lock()
		exists := false
		for i := range s.Rooms {
			if s.Rooms[i].name == channelName {
				// Update topic if persisted.
				if ch.Topic != "" {
					s.Rooms[i].roomMux.Lock()
					s.Rooms[i].topic = ch.Topic
					s.Rooms[i].roomMux.Unlock()
				}
				exists = true
				break
			}
		}
		if !exists {
			room := s.createRoom(channelName)
			if ch.Topic != "" {
				room.topic = ch.Topic
			}
			if ch.Registered {
				room.registered = true
			}
			// Restore persisted modes on top of defaults.
			if ch.Modes != "" {
				applyModeString(&room, ch.Modes)
			}
			s.Rooms = append(s.Rooms, room)
			log.Infof("[hydrate] Restored channel #%s from Redis (topic: %q, modes: %q)", channelName, ch.Topic, ch.Modes)
		}
		s.RoomsMutex.Unlock()
	}
	return nil
}

// pingClient sends a PING to the given connection every PingInterval.
// It closes the connection if no PONG is received within 2*PingInterval.
// The goroutine exits when the connection is closed.
func (s *Server) pingClient(conn net.Conn, clientID uuid.UUID) {
	if s.PingInterval == 0 {
		return
	}
	ticker := time.NewTicker(s.PingInterval)
	defer ticker.Stop()

	for range ticker.C {
		// Check if client is still registered.
		s.ClientsMutex.Lock()
		found := false
		for _, c := range s.Clients {
			if c.identifier == clientID {
				found = true
				break
			}
		}
		s.ClientsMutex.Unlock()
		if !found {
			return
		}

		pingMsg := fmt.Sprintf("PING :%s\r\n", s.getConfig().Server.Host)
		conn.SetWriteDeadline(time.Now().Add(s.PingInterval))
		if _, err := conn.Write([]byte(pingMsg)); err != nil {
			return
		}

		// Set read deadline to 2*PingInterval so the main read loop times out
		// if no PONG arrives.
		conn.SetReadDeadline(time.Now().Add(2 * s.PingInterval))
	}
}

// startHeartbeat sends periodic heartbeats to Redis to signal this pod is alive
func (s *Server) startHeartbeat() {
	if s.RedisClient == nil {
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	log.Println("Started heartbeat goroutine (10s interval)")

	for {
		select {
		case <-s.redisCtx.Done():
			log.Println("Heartbeat goroutine stopped")
			return
		case <-ticker.C:
			s.ClientsMutex.Lock()
			clientCount := len(s.Clients)
			s.ClientsMutex.Unlock()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := s.RedisClient.Heartbeat(ctx, clientCount, version.GetVersion()); err != nil {
				log.Errorf("Failed to send heartbeat: %v", err)
			} else {
				if s.getConfig().Server.Debug {
		log.Debugf("Heartbeat sent (clients: %d)", clientCount)
	}
			}
			cancel()
		}
	}
}

// startOrphanCleanup periodically cleans up orphaned clients (leader only)
func (s *Server) startOrphanCleanup() {
	if s.RedisClient == nil {
		return
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	log.Println("Started orphan cleanup goroutine (30s interval)")

	for {
		select {
		case <-s.redisCtx.Done():
			log.Println("Orphan cleanup goroutine stopped")
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

			// Try to become leader
			isLeader, err := s.RedisClient.AcquireLeaderLock(ctx, 30*time.Second)
			if err != nil {
				log.Errorf("Failed to acquire leader lock: %v", err)
				cancel()
				continue
			}

			if isLeader {
				if s.getConfig().Server.Debug {
		log.Debugf("This pod is the leader, performing cleanup...")
	}
				// We are the leader, perform cleanup
				count, err := s.RedisClient.CleanupOrphanedClients(ctx)
				if err != nil {
					log.Errorf("Failed to cleanup orphaned clients: %v", err)
				} else if count > 0 {
					log.Infof("Cleaned up %d orphaned clients", count)
				} else {
					if s.getConfig().Server.Debug {
		log.Debugf("No orphaned clients found")
	}
				}

				// Release lock
				if err := s.RedisClient.ReleaseLeaderLock(ctx); err != nil {
					log.Errorf("Failed to release leader lock: %v", err)
				}
			} else {
				if s.getConfig().Server.Debug {
		log.Debugf("Another pod is the leader, skipping cleanup")
	}
			}

			cancel()
		}
	}
}

// subscribeToRedisEvents subscribes to Redis pub/sub events from other pods
func (s *Server) subscribeToRedisEvents() {
	if s.RedisClient == nil {
		return
	}

	eventChan, err := s.RedisClient.SubscribeEvents(s.redisCtx)
	if err != nil {
		log.Errorf("Failed to subscribe to Redis events: %v", err)
		return
	}

	log.Println("Subscribed to Redis events, listening for cross-pod messages...")

	for event := range eventChan {
		if s.getConfig().Server.Debug {
		log.Debugf("Received Redis event: %s", event.Type)
	}

		switch event.Type {
		case "PRIVMSG":
			s.handleRedisPrivmsg(event)
		case "JOIN":
			s.handleRedisJoin(event)
		case "PART":
			s.handleRedisPart(event)
		case "QUIT":
			s.handleRedisQuit(event)
		case "NICK":
			s.handleRedisNick(event)
		case "KICK":
			s.handleRedisKick(event)
		default:
			if s.getConfig().Server.Debug {
		log.Debugf("Unknown Redis event type: %s", event.Type)
	}
		}
	}

	log.Println("Redis event subscription ended")
}

// redisStr extracts a string value from an event data map.
func redisStr(data map[string]interface{}, key string) string {
	if v, ok := data[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// handleRedisPrivmsg handles a PRIVMSG event received from another pod.
// It delivers the message to local clients that are in the target channel/user.
// The event data contains: from_nick, from_user, target, message, pod_id.
// We skip events that originated from this pod to prevent loops.
func (s *Server) handleRedisPrivmsg(event *redis.Event) {
	fromNick := redisStr(event.Data, "from_nick")
	fromUser := redisStr(event.Data, "from_user")
	target := redisStr(event.Data, "target")
	message := redisStr(event.Data, "message")
	originPod := redisStr(event.Data, "pod_id")

	if originPod == s.RedisClient.PodID() {
		return // skip events we published
	}

	if fromNick == "" || target == "" {
		return
	}

	cfg := s.getConfig()
	privmsgLine := fmt.Sprintf(":%s!%s@%s PRIVMSG %s :%s\r\n",
		fromNick, fromUser, cfg.Server.Host, target, message)

	if strings.HasPrefix(target, "#") {
		// Channel message: deliver to all local clients in this channel.
		channelName := target[1:]
		s.RoomsMutex.Lock()
		var room *Room
		for i := range s.Rooms {
			if s.Rooms[i].name == channelName {
				room = &s.Rooms[i]
				break
			}
		}
		s.RoomsMutex.Unlock()
		if room == nil {
			return
		}
		room.roomMux.Lock()
		memberIDs := make([]uuid.UUID, 0, len(room.clients))
		for id := range room.clients {
			memberIDs = append(memberIDs, id)
		}
		room.roomMux.Unlock()

		s.ClientsMutex.Lock()
		for _, id := range memberIDs {
			for i := range s.Clients {
				if s.Clients[i].identifier == id && s.Clients[i].conn != nil {
					s.Clients[i].conn.Write([]byte(privmsgLine))
					break
				}
			}
		}
		s.ClientsMutex.Unlock()
	} else {
		// Direct message to a specific nick on this pod.
		s.ClientsMutex.Lock()
		for i := range s.Clients {
			if strings.EqualFold(s.Clients[i].nick, target) && s.Clients[i].conn != nil {
				s.Clients[i].conn.Write([]byte(privmsgLine))
				break
			}
		}
		s.ClientsMutex.Unlock()
	}
}

// handleRedisJoin handles a JOIN event from another pod.
// It updates local room state so our clients see the remote client in the channel.
// Event data: uuid, nick, user, channel, pod_id.
func (s *Server) handleRedisJoin(event *redis.Event) {
	originPod := redisStr(event.Data, "pod_id")
	if originPod == s.RedisClient.PodID() {
		return
	}
	remoteNick := redisStr(event.Data, "nick")
	remoteUser := redisStr(event.Data, "user")
	channel := redisStr(event.Data, "channel")
	if remoteNick == "" || channel == "" {
		return
	}

	channelName := strings.TrimPrefix(channel, "#")
	cfg := s.getConfig()
	joinLine := fmt.Sprintf(":%s!%s@%s JOIN %s\r\n", remoteNick, remoteUser, cfg.Server.Host, channel)

	// Notify all local clients in that channel.
	s.RoomsMutex.Lock()
	var room *Room
	for i := range s.Rooms {
		if s.Rooms[i].name == channelName {
			room = &s.Rooms[i]
			break
		}
	}
	s.RoomsMutex.Unlock()
	if room == nil {
		return
	}

	room.roomMux.Lock()
	memberIDs := make([]uuid.UUID, 0, len(room.clients))
	for id := range room.clients {
		memberIDs = append(memberIDs, id)
	}
	room.roomMux.Unlock()

	s.ClientsMutex.Lock()
	for _, id := range memberIDs {
		for i := range s.Clients {
			if s.Clients[i].identifier == id && s.Clients[i].conn != nil {
				s.Clients[i].conn.Write([]byte(joinLine))
				break
			}
		}
	}
	s.ClientsMutex.Unlock()
}

// handleRedisPart handles a PART event from another pod.
// Event data: uuid, nick, user, channel, message, pod_id.
func (s *Server) handleRedisPart(event *redis.Event) {
	originPod := redisStr(event.Data, "pod_id")
	if originPod == s.RedisClient.PodID() {
		return
	}
	remoteNick := redisStr(event.Data, "nick")
	remoteUser := redisStr(event.Data, "user")
	channel := redisStr(event.Data, "channel")
	message := redisStr(event.Data, "message")
	if remoteNick == "" || channel == "" {
		return
	}

	channelName := strings.TrimPrefix(channel, "#")
	cfg := s.getConfig()
	partLine := fmt.Sprintf(":%s!%s@%s PART %s :%s\r\n", remoteNick, remoteUser, cfg.Server.Host, channel, message)

	s.RoomsMutex.Lock()
	var room *Room
	for i := range s.Rooms {
		if s.Rooms[i].name == channelName {
			room = &s.Rooms[i]
			break
		}
	}
	s.RoomsMutex.Unlock()
	if room == nil {
		return
	}

	room.roomMux.Lock()
	memberIDs := make([]uuid.UUID, 0, len(room.clients))
	for id := range room.clients {
		memberIDs = append(memberIDs, id)
	}
	room.roomMux.Unlock()

	s.ClientsMutex.Lock()
	for _, id := range memberIDs {
		for i := range s.Clients {
			if s.Clients[i].identifier == id && s.Clients[i].conn != nil {
				s.Clients[i].conn.Write([]byte(partLine))
				break
			}
		}
	}
	s.ClientsMutex.Unlock()
}

// handleRedisQuit handles a QUIT event from another pod.
// Event data: uuid, nick, user, reason, pod_id.
//
// Per RFC, QUIT should go only to clients sharing a channel with the quitter.
// Cross-pod events do not carry the remote client's channel list, so we send
// to all local registered clients as an approximation.
func (s *Server) handleRedisQuit(event *redis.Event) {
	originPod := redisStr(event.Data, "pod_id")
	if originPod == s.RedisClient.PodID() {
		return
	}
	remoteNick := redisStr(event.Data, "nick")
	remoteUser := redisStr(event.Data, "user")
	reason := redisStr(event.Data, "reason")
	if remoteNick == "" {
		return
	}

	cfg := s.getConfig()
	quitLine := fmt.Sprintf(":%s!%s@%s QUIT :%s\r\n", remoteNick, remoteUser, cfg.Server.Host, reason)

	s.ClientsMutex.Lock()
	for i := range s.Clients {
		if s.Clients[i].conn != nil {
			s.Clients[i].conn.Write([]byte(quitLine))
		}
	}
	s.ClientsMutex.Unlock()
}

// handleRedisNick handles a NICK change event from another pod.
// Event data: uuid, old_nick, new_nick, user, pod_id.
func (s *Server) handleRedisNick(event *redis.Event) {
	originPod := redisStr(event.Data, "pod_id")
	if originPod == s.RedisClient.PodID() {
		return
	}
	oldNick := redisStr(event.Data, "old_nick")
	newNick := redisStr(event.Data, "new_nick")
	user := redisStr(event.Data, "user")
	if oldNick == "" || newNick == "" {
		return
	}

	cfg := s.getConfig()
	nickLine := fmt.Sprintf(":%s!%s@%s NICK :%s\r\n", oldNick, user, cfg.Server.Host, newNick)

	// Notify all local clients.
	s.ClientsMutex.Lock()
	for i := range s.Clients {
		if s.Clients[i].conn != nil {
			s.Clients[i].conn.Write([]byte(nickLine))
		}
	}
	s.ClientsMutex.Unlock()
}

// handleRedisKick handles a KICK event from another pod.
// Event data: from_nick, from_user, target_nick, channel, reason, pod_id.
func (s *Server) handleRedisKick(event *redis.Event) {
	originPod := redisStr(event.Data, "pod_id")
	if originPod == s.RedisClient.PodID() {
		return
	}
	fromNick := redisStr(event.Data, "from_nick")
	fromUser := redisStr(event.Data, "from_user")
	targetNick := redisStr(event.Data, "target_nick")
	channel := redisStr(event.Data, "channel")
	reason := redisStr(event.Data, "reason")
	if fromNick == "" || channel == "" || targetNick == "" {
		return
	}

	channelName := strings.TrimPrefix(channel, "#")
	cfg := s.getConfig()
	kickLine := fmt.Sprintf(":%s!%s@%s KICK %s %s :%s\r\n",
		fromNick, fromUser, cfg.Server.Host, channel, targetNick, reason)

	s.RoomsMutex.Lock()
	var room *Room
	for i := range s.Rooms {
		if s.Rooms[i].name == channelName {
			room = &s.Rooms[i]
			break
		}
	}
	s.RoomsMutex.Unlock()
	if room == nil {
		return
	}

	room.roomMux.Lock()
	memberIDs := make([]uuid.UUID, 0, len(room.clients))
	for id := range room.clients {
		memberIDs = append(memberIDs, id)
	}
	room.roomMux.Unlock()

	s.ClientsMutex.Lock()
	for _, id := range memberIDs {
		for i := range s.Clients {
			if s.Clients[i].identifier == id && s.Clients[i].conn != nil {
				s.Clients[i].conn.Write([]byte(kickLine))
				break
			}
		}
	}
	s.ClientsMutex.Unlock()
}
