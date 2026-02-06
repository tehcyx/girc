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
	"strconv"
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

	// Redis client for distributed state management (nil if disabled)
	RedisClient *redis.Client

	// Context for cancelling Redis event subscription
	redisCtx    context.Context
	redisCancel context.CancelFunc
}

//New creates a new server, listening on the defined port and starting to accept client connections
func New() {
	log.Println("Launching server...")

	// listen on all interfaces
	ln, err := net.Listen("tcp", fmt.Sprintf(":%s", config.Values.Server.Port))
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
	}

	// Initialize Redis client if enabled
	if config.Values.Redis.Enabled {
		log.Println("Redis enabled, initializing distributed state management...")

		// Generate pod ID if not provided
		podID := config.Values.Redis.PodID
		if podID == "" {
			podID = uuid.Must(uuid.NewRandom()).String()
			log.Printf("Generated pod ID: %s", podID)
		}

		redisClient, err := redis.NewClient(config.Values.Redis.URL, podID)
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

func (s *Server) initRooms() {
	lobby := createRoom("lobby")

	s.RoomsMutex.Lock()
	defer s.RoomsMutex.Unlock()

	s.Rooms = append(s.Rooms, lobby)
	s.Lobby = &lobby
}

func (s *Server) handleClientConnect(conn net.Conn) {
	log.Printf("Client connecting, handling connection ...\n")
	identifier := uuid.Must(uuid.NewRandom())
	if config.Values.Server.Debug {
		log.Debugf("This is concurrent user #%d, generated UUID is: %s\n", len(s.Clients), identifier)
	}

	pass := true // Allow PASS command before registration
	initCompleted := false

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
			if config.Values.Server.Debug {
		log.Debugf("Sent ERROR to %s: %s", client.nick, disconnectReason)
	}
		}

		// Send QUIT notifications to all users in the same channels
		// This lets other users know this client has left
		if client.nick != "" {
			quitMsg := fmt.Sprintf(":%s!%s@%s QUIT :%s\r\n",
				client.nick, client.user, config.Values.Server.Host, disconnectReason)

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
				if config.Values.Server.Debug {
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
			if config.Values.Server.Debug {
				log.Infof("Received message from client: %s", message)
			}
			message = strings.TrimSpace(message)
			tokens := strings.Split(message, " ")

			cmd := strings.ToUpper(tokens[0]) // Make command case-insensitive
			args := strings.Join(tokens[1:], " ")

			// Log command parsing, but sanitize sensitive commands (PASS, REGISTER, SETPASS)
			if config.Values.Server.Debug {
				if cmd == PassCmd || cmd == RegisterCmd || cmd == SetPassCmd {
					log.Infof("Parsed command: '%s', args: [REDACTED]", cmd)
				} else {
					log.Infof("Parsed command: '%s', args: '%s'", cmd, args)
				}
			}

			if len(cmd) == 0 {
				continue
			} else if cmd == "CAP" {
				// Ignore CAP commands - we don't support capabilities
				// Modern clients send this, but we just silently ignore it
				log.Infof("CAP command ignored: %s", args)
				continue
			} else if cmd == QuitCmd {
				// send goodbye message
				return
			} else if cmd == PassCmd && pass {
				// Command: PASS Parameters: <password>
				// Store password for authentication during NICK/USER registration
				log.Infof("Processing PASS command")
				if len(args) == 0 {
					log.Infof("PASS: No password given")
					send(conn, ":%s %s PASS :Not enough parameters\n", config.Values.Server.Host, ErrNeedMoreParams)
					pass = false
					continue
				}
				client.password = args
				log.Infof("PASS: Password stored for authentication")
				pass = false
				continue
			} else if cmd == RegisterCmd {
				// Command: REGISTER Parameters: <username> <password> <email>
				// Example: REGISTER alice mySecurePass123 alice@example.com
				log.Infof("Processing REGISTER command")

				parts := strings.Fields(args)
				if len(parts) < 3 {
					log.Infof("REGISTER: Not enough parameters")
					send(conn, ":%s %s %s :Not enough parameters (usage: REGISTER <username> <password> <email>)\n",
						config.Values.Server.Host, ErrNeedMoreParams, RegisterCmd)
					continue
				}

				username := parts[0]
				password := parts[1]
				email := parts[2]

				log.Infof("REGISTER: Attempting to register user '%s' with email '%s'", username, email)

				// Validate username (use same rules as nickname)
				if !validNickCharset(username) {
					log.Infof("REGISTER: Invalid username charset: %s", username)
					send(conn, ":%s %s :Invalid username (must contain only letters, numbers, -, _, [, ], \\, `, ^, {, })\n",
						config.Values.Server.Host, ErrNickInvalid)
					continue
				}

				// Validate password strength
				if !IsValidPassword(password) {
					log.Infof("REGISTER: Weak password for user '%s'", username)
					send(conn, ":%s %s :Password does not meet security requirements (minimum 8 characters, maximum 72 characters)\n",
						config.Values.Server.Host, ErrWeakPassword)
					continue
				}

				// Validate email (basic validation)
				if !IsValidEmail(email) {
					log.Infof("REGISTER: Invalid email for user '%s': %s", username, email)
					send(conn, ":%s %s :Invalid email address\n",
						config.Values.Server.Host, ErrInvalidEmail)
					continue
				}

				// Check if username already exists as a registered account
				if s.RedisClient != nil {
					ctx := context.Background()
					// Check if user account exists (not just if nickname is in use by connected client)
					existingUser, err := s.RedisClient.GetUser(ctx, username)
					if err == nil && existingUser != nil {
						// User account already exists
						log.Infof("REGISTER: Username '%s' already registered", username)
						send(conn, ":%s %s :Username already registered\n",
							config.Values.Server.Host, ErrRegisteredAlready)
						continue
					}
					// Error could mean user not found (expected) or Redis error
					// If it's just "user not found", that's fine - continue with registration
				} else {
					log.Warn("REGISTER: Redis not available, cannot persist user registration")
					send(conn, ":%s %s :Registration service not available\n",
						config.Values.Server.Host, ErrNoSuchService)
					continue
				}

				// Hash the password
				hashedPassword, err := HashPassword(password)
				if err != nil {
					log.Errorf("REGISTER: Failed to hash password for user '%s': %v", username, err)
					send(conn, ":%s %s :Registration failed - please try again later\n",
						config.Values.Server.Host, ErrNoSuchService)
					continue
				}

				// Store user in Redis
				ctx := context.Background()
				err = s.RedisClient.StoreUser(ctx, username, hashedPassword, email)
				if err != nil {
					log.Errorf("REGISTER: Failed to store user '%s': %v", username, err)
					send(conn, ":%s %s :Registration failed - please try again later\n",
						config.Values.Server.Host, ErrNoSuchService)
					continue
				}

				log.Infof("REGISTER: Successfully registered user '%s'", username)
				send(conn, ":%s %s %s %s :Account created successfully. To login: send PASS <password>, then NICK %s, then USER %s 0 * :Your Name\n",
					config.Values.Server.Host, RplRegistered, username, username, username, username)
				continue
			} else if cmd == NickCmd && !pass {
				// Command: NICK Parameters: <nickname> [ <hopcount> ]
				log.Infof("Processing NICK command with args: '%s'", args)
				if len(args) == 0 {
					log.Infof("NICK: No nickname given")
					send(conn, ":%s %s :No nickname given\n", config.Values.Server.Host, ErrNickNull)
					continue
				}
				if !validNickCharset(args) {
					log.Infof("NICK: Invalid nickname charset: %s", args)
					send(conn, ":%s %s * %s :Erroneous nickname\n", config.Values.Server.Host, ErrNickInvalid, args)
					continue
				}
				// Check nick availability - local and Redis if enabled
				localExists := s.ClientExists(args)
				redisExists := false

				if s.RedisClient != nil {
					ctx := context.Background()
					available, err := s.RedisClient.IsNickAvailable(ctx, args)
					if err != nil {
						log.Errorf("Failed to check nick availability in Redis: %v", err)
						// Continue with local check only
					} else {
						redisExists = !available
					}
				}

				if localExists || redisExists {
					log.Infof("NICK: Nickname '%s' already in use", args)
					send(conn, ":%s %s * %s :Nickname is already in use\n", config.Values.Server.Host, ErrNickInUse, args)
				} else {
					log.Infof("NICK: Setting nickname to '%s'", args)
					client.nick = args
					client.user = "*"
					continue
				}
			} else if cmd == UserCmd && !pass {
				// Command: USER Parameters: <user> <mode> <unused> <realname>
				// Example: USER shout-user 0 * :Shout User
				log.Infof("Processing USER command with args: '%s'", args)
				if len(args) == 0 {
					log.Infof("USER: No parameters given")
					send(conn, ":%s %s %s:You may not reregister\n", config.Values.Server.Host, ErrAlreadyRegistered, client.nick)
					continue
				}
				split := strings.Split(args, ":")
				log.Infof("USER: split into %d parts", len(split))
				if len(split) < 2 || len(strings.Split(split[0], " ")) < 3 {
					log.Infof("USER: Not enough parameters")
					send(conn, ":%s %s %s USER :Not enough parameters\n", config.Values.Server.Host, ErrNeedMoreParams, client.nick)
					continue
				}
				args1 := split[0]
				args2 := split[1]

				client.user = strings.Split(args1, " ")[0]
				client.mode, _ = strconv.Atoi(strings.Split(args1, " ")[1])
				client.unused = strings.Split(args1, " ")[2]
				client.realname = args2

				// Attempt authentication if Redis is available and user provided password
				if s.RedisClient != nil && client.password != "" {
					ctx := context.Background()
					// Check if this nickname is a registered account
					userData, err := s.RedisClient.GetUser(ctx, client.nick)
					if err == nil && userData != nil {
						// Account exists, verify password
						log.Infof("USER: Found registered account for '%s', verifying password", client.nick)
						if err := VerifyPassword(userData.HashedPassword, client.password); err == nil {
							// Authentication successful
							client.authenticated = true
							client.accountName = client.nick
							// Update last login time
							if err := s.RedisClient.UpdateLastLogin(ctx, client.nick); err != nil {
								log.Errorf("USER: Failed to update last login for '%s': %v", client.nick, err)
							}
							log.Infof("USER: Successfully authenticated '%s' with registered account", client.nick)
							send(conn, ":%s %s %s %s :You are now logged in as %s\n",
								config.Values.Server.Host, RplLoggedIn, client.nick, client.accountName, client.accountName)
						} else {
							// Authentication failed
							log.Warnf("USER: Password verification failed for registered account '%s'", client.nick)
							send(conn, ":%s %s :Password incorrect\n", config.Values.Server.Host, ErrPasswdMismatch)
							// Clear password and continue without authentication
							client.password = ""
						}
					} else {
						// Not a registered account, continue normally
						log.Infof("USER: '%s' is not a registered account, proceeding without authentication", client.nick)
					}
					// Clear the password from memory after authentication attempt
					client.password = ""
				}

				log.Infof("USER: Calling AddClient for nick='%s', user='%s', realname='%s', authenticated=%v",
					client.nick, client.user, client.realname, client.authenticated)
				s.AddClient(client)
				initCompleted = true
				log.Infof("USER: Registration completed")
			} else if cmd == PingCmd && initCompleted {
				if len(args) == 0 {
					send(conn, ":%s %s :No origin specified\n", config.Values.Server.Host, ErrNoOrigin)
					continue
				}
				origins := strings.Split(args, " ")
				if len(origins) == 1 {
					send(conn, ":%s %s %s\n", config.Values.Server.Host, PingPongCmd, args)
				} else {
					// query other server as well for now just answer
					// ...
					send(conn, ":%s %s %s\n", config.Values.Server.Host, PingPongCmd, args)
				}
			} else if cmd == JoinCmd && initCompleted {
				if len(args) == 0 {
					send(conn, ":%s %s %s :Not enough parameters\n", config.Values.Server.Host, ErrNeedMoreParams, JoinCmd)
					continue
				}
				rooms := strings.Split(args, ",")
				for _, roomName := range rooms {
					s.JoinRoomByName(client, roomName)
				}
			} else if cmd == PrivmsgCmd && initCompleted {
				if len(args) == 0 {
					send(conn, ":%s %s :No recipient given\n", config.Values.Server.Host, ErrNoRecipient)
					continue
				}
				parts := strings.Split(args, ":")
				if len(parts) < 2 {
					send(conn, ":%s %s :No text to send\n", config.Values.Server.Host, ErrNoTextToSend)
					continue
				}
				target := strings.TrimSpace(parts[0])
				chatMessage := strings.TrimSpace(parts[1])
				if strings.HasPrefix(target, "#") || strings.HasPrefix(target, "$") {
					// is channel or mask
					if validMessageMask(target) {

						fmt.Printf("Masking is not implemented, so we just swallow that message: %s\n", args)
						// ERR_WILDTOPLEVEL
						// ERR_NOTOPLEVEL
						// ERR_TOOMANYTARGETS
					} else {
						if inChannel, room := s.IsUserInRoom(client, target); inChannel {
							room.roomMux.Lock()
							for ident := range room.clients {
								if ident == client.identifier {
									continue
								}
								for _, cli := range s.Clients {
									if ident == cli.identifier {
										send(cli.conn, ":%s!%s@%s %s %s :%s\n", client.nick, client.user, config.Values.Server.Host, PrivmsgCmd, target, chatMessage)
									}
								}
							}
							room.roomMux.Unlock()
						} else {
							send(conn, ":%s %s %s %s :Cannot send to channel\n", config.Values.Server.Host, ErrCannotSendToChan, client.nick, target)
						}
					}
				} else if validNickCharset(target) {
					// not a channel or mask, let's find out if the requested user is connected
					targetClient := s.ClientByName(target)
					if targetClient.nick == "" {
						send(conn, ":%s %s %s %s :No such nick/channel\n", config.Values.Server.Host, ErrNoSuchNick, client.nick, target)
					} else {
						send(targetClient.conn, ":%s!%s@%s %s %s :%s\n", client.nick, client.user, config.Values.Server.Host, PrivmsgCmd, target, chatMessage)
					}
				}
			} else if cmd == PartCmd && initCompleted {
				if len(args) == 0 {
					send(conn, ":%s %s :No recipient given\n", config.Values.Server.Host, ErrNoRecipient)
					continue
				}
				parts := strings.Split(args, ":")
				partTargets := strings.Split(strings.TrimSpace(parts[0]), ",")
				partMessage := ""
				if len(parts) >= 2 {
					partMessage = strings.TrimSpace(parts[1])
				}

				for _, target := range partTargets {
					if s.RoomExists(target) {
						if inChannel, room := s.IsUserInRoom(client, target); inChannel {
							room.roomMux.Lock()
							defer room.roomMux.Unlock()
							for ident := range room.clients {
								fmt.Printf("exists %d, %s, %s, %s\n", 2, room.name, ident, client.identifier)
								if ident != client.identifier {
									continue
								}
								for _, cli := range s.Clients {
									send(cli.conn, ":%s!%s@%s %s %s :%s\n", client.nick, client.user, config.Values.Server.Host, PartCmd, target, partMessage)
									if ident == cli.identifier {
										delete(room.clients, client.identifier) // remove user from room
										s.ClientsMutex.Lock()
										defer s.ClientsMutex.Unlock()
										delete(client.rooms, room.identifier) // remove room from users rooms list
									}
								}
							}
						} else {
							send(conn, ":%s %s %s :You're not on that channel\n", config.Values.Server.Host, ErrNotOnChannel, target)
						}
					} else {
						send(conn, ":%s %s %s :No such channel\n", config.Values.Server.Host, ErrNoSuchChannel, target)
					}
				}
			} else if cmd == WhoisCmd && initCompleted {
				// Command: WHOIS <nickname>
				// Returns information about the specified user
				if len(args) == 0 {
					send(conn, ":%s %s :No nickname given\n", config.Values.Server.Host, ErrNoRecipient)
					continue
				}
				targetNick := strings.TrimSpace(args)

				// Find the target client
				s.ClientsMutex.Lock()
				var targetClient *Client
				for i := range s.Clients {
					if s.Clients[i].nick == targetNick {
						targetClient = &s.Clients[i]
						break
					}
				}
				s.ClientsMutex.Unlock()

				if targetClient == nil {
					// User not found
					send(conn, ":%s %s %s :No such nick/channel\n", config.Values.Server.Host, ErrNoSuchNick, targetNick)
				} else {
					// RPL_WHOISUSER: 311 <nick> <user> <host> * :<real name>
					send(conn, ":%s %s %s %s %s %s * :%s\n",
						config.Values.Server.Host, RplWhoisUser, client.nick,
						targetClient.nick, targetClient.user, config.Values.Server.Host, targetClient.realname)

					// RPL_WHOISCHANNELS: 319 <nick> :<channels>
					if len(targetClient.rooms) > 0 {
						var channels []string
						for roomID := range targetClient.rooms {
							for _, room := range s.Rooms {
								if room.identifier == roomID {
									// Check if user is operator in this room
									isOp := false
									room.roomMux.Lock()
									if room.operators != nil {
										_, isOp = room.operators[targetClient.identifier]
									}
									room.roomMux.Unlock()

									channelName := "#" + room.name
									if isOp {
										channelName = "@" + channelName
									}
									channels = append(channels, channelName)
									break
								}
							}
						}
						if len(channels) > 0 {
							send(conn, ":%s %s %s %s :%s\n",
								config.Values.Server.Host, RplWhoIsChannels, client.nick,
								targetClient.nick, strings.Join(channels, " "))
						}
					}

					// RPL_WHOISSERVER: 312 <nick> <server> :<server info>
					send(conn, ":%s %s %s %s %s :%s\n",
						config.Values.Server.Host, RplWhoisServer, client.nick,
						targetClient.nick, config.Values.Server.Name, config.Values.Server.Host)

					// RPL_WHOISOPERATOR: 313 <nick> :is an IRC operator (if applicable)
					if targetClient.operator {
						send(conn, ":%s %s %s %s :is an IRC operator\n",
							config.Values.Server.Host, RplWhoisOperator, client.nick, targetClient.nick)
					}

					// RPL_ENDOFWHOIS: 318 <nick> :End of WHOIS list
					send(conn, ":%s %s %s %s :End of WHOIS list\n",
						config.Values.Server.Host, RplEndOfWhois, client.nick, targetClient.nick)
				}
			} else if cmd == KickCmd && initCompleted {
				// Command: KICK <channel> <user> [<comment>]
				// Kicks a user out of a channel (requires operator privileges)
				if len(args) == 0 {
					send(conn, ":%s %s %s :Not enough parameters\n", config.Values.Server.Host, ErrNeedMoreParams, KickCmd)
					continue
				}
				parts := strings.SplitN(args, " ", 2)
				if len(parts) < 2 {
					send(conn, ":%s %s %s :Not enough parameters\n", config.Values.Server.Host, ErrNeedMoreParams, KickCmd)
					continue
				}

				channelName := strings.TrimSpace(parts[0])
				remainder := parts[1]

				// Parse the target nick and optional kick reason
				var targetNick, kickReason string
				if strings.Contains(remainder, ":") {
					kickParts := strings.SplitN(remainder, ":", 2)
					targetNick = strings.TrimSpace(kickParts[0])
					if len(kickParts) > 1 {
						kickReason = kickParts[1]
					}
				} else {
					targetNick = strings.TrimSpace(remainder)
				}

				if kickReason == "" {
					kickReason = client.nick // Default kick reason is kicker's nick
				}

				// Check if channel exists
				if !s.RoomExists(channelName) {
					send(conn, ":%s %s %s :No such channel\n", config.Values.Server.Host, ErrNoSuchChannel, channelName)
					continue
				}

				// Check if kicker is in the channel
				inChannel, room := s.IsUserInRoom(client, channelName)
				if !inChannel {
					send(conn, ":%s %s %s :You're not on that channel\n", config.Values.Server.Host, ErrNotOnChannel, channelName)
					continue
				}

				// Check if kicker has operator privileges
				room.roomMux.Lock()
				isOp := false
				isFounder := false
				if room.operators != nil {
					_, isOp = room.operators[client.identifier]
				}
				if room.founders != nil {
					_, isFounder = room.founders[client.identifier]
				}
				room.roomMux.Unlock()

				if !isOp && !isFounder {
					send(conn, ":%s %s %s :You're not channel operator\n", config.Values.Server.Host, ErrChanOpPrivsNeeded, channelName)
					continue
				}

				// Find the target client
				s.ClientsMutex.Lock()
				var targetClient *Client
				for i := range s.Clients {
					if s.Clients[i].nick == targetNick {
						targetClient = &s.Clients[i]
						break
					}
				}
				s.ClientsMutex.Unlock()

				if targetClient == nil {
					send(conn, ":%s %s %s :No such nick/channel\n", config.Values.Server.Host, ErrNoSuchNick, targetNick)
					continue
				}

				// Check if target is in the channel
				targetInChannel := false
				room.roomMux.Lock()
				if room.clients != nil {
					_, targetInChannel = room.clients[targetClient.identifier]
				}
				room.roomMux.Unlock()

				if !targetInChannel {
					send(conn, ":%s %s %s %s :They aren't on that channel\n",
						config.Values.Server.Host, ErrUserNotInChannel, targetNick, channelName)
					continue
				}

				// Perform the kick - notify all channel members
				room.roomMux.Lock()
				for memberID := range room.clients {
					for i := range s.Clients {
						if s.Clients[i].identifier == memberID {
							send(s.Clients[i].conn, ":%s!%s@%s KICK %s %s :%s\n",
								client.nick, client.user, config.Values.Server.Host,
								channelName, targetNick, kickReason)
							break
						}
					}
				}

				// Remove target from channel
				delete(room.clients, targetClient.identifier)
				if room.operators != nil {
					delete(room.operators, targetClient.identifier)
				}
				room.roomMux.Unlock()

				// Remove channel from target's room list
				s.ClientsMutex.Lock()
				for i := range s.Clients {
					if s.Clients[i].identifier == targetClient.identifier {
						delete(s.Clients[i].rooms, room.identifier)
						break
					}
				}
				s.ClientsMutex.Unlock()

				log.Infof("[KICK] %s kicked %s from %s: %s", client.nick, targetNick, channelName, kickReason)
			} else if cmd == InviteCmd && initCompleted {
				// Command: INVITE <nickname> <channel>
				// Invites a user to a channel
				if len(args) == 0 {
					send(conn, ":%s %s %s :Not enough parameters\n", config.Values.Server.Host, ErrNeedMoreParams, InviteCmd)
					continue
				}
				parts := strings.SplitN(args, " ", 2)
				if len(parts) < 2 {
					send(conn, ":%s %s %s :Not enough parameters\n", config.Values.Server.Host, ErrNeedMoreParams, InviteCmd)
					continue
				}

				targetNick := strings.TrimSpace(parts[0])
				channelName := strings.TrimSpace(parts[1])

				// Check if channel exists
				if !s.RoomExists(channelName) {
					send(conn, ":%s %s %s :No such channel\n", config.Values.Server.Host, ErrNoSuchChannel, channelName)
					continue
				}

				// Check if inviter is in the channel
				inChannel, room := s.IsUserInRoom(client, channelName)
				if !inChannel {
					send(conn, ":%s %s %s :You're not on that channel\n", config.Values.Server.Host, ErrNotOnChannel, channelName)
					continue
				}

				// Find the target client
				s.ClientsMutex.Lock()
				var targetClient *Client
				for i := range s.Clients {
					if s.Clients[i].nick == targetNick {
						targetClient = &s.Clients[i]
						break
					}
				}
				s.ClientsMutex.Unlock()

				if targetClient == nil {
					send(conn, ":%s %s %s :No such nick/channel\n", config.Values.Server.Host, ErrNoSuchNick, targetNick)
					continue
				}

				// Check if target is already in the channel
				targetInChannel := false
				room.roomMux.Lock()
				if room.clients != nil {
					_, targetInChannel = room.clients[targetClient.identifier]
				}
				room.roomMux.Unlock()

				if targetInChannel {
					send(conn, ":%s %s %s %s :is already on channel\n",
						config.Values.Server.Host, ErrUserOnChannel, targetNick, channelName)
					continue
				}

				// Send INVITE notification to target user
				send(targetClient.conn, ":%s!%s@%s INVITE %s %s\n",
					client.nick, client.user, config.Values.Server.Host,
					targetNick, channelName)

				// Send RPL_INVITING confirmation to inviter
				send(conn, ":%s %s %s %s %s\n",
					config.Values.Server.Host, RplInviting, client.nick,
					channelName, targetNick)

				log.Infof("[INVITE] %s invited %s to %s", client.nick, targetNick, channelName)
			} else if cmd == ListCmd && initCompleted {
				// Command: LIST [<channel>{,<channel>}]
				// Lists channels and their topics
				log.Infof("[LIST] %s requested channel list: '%s'", client.nick, args)

				var channelsToList []string

				if len(args) == 0 {
					// List all channels
					s.RoomsMutex.Lock()
					for i := range s.Rooms {
						// Skip lobby unless explicitly requested
						if s.Rooms[i].name != "lobby" {
							channelsToList = append(channelsToList, s.Rooms[i].name)
						}
					}
					s.RoomsMutex.Unlock()
				} else {
					// List specific channels (comma-separated)
					channels := strings.Split(args, ",")
					for _, ch := range channels {
						channelName := strings.TrimSpace(ch)
						if strings.HasPrefix(channelName, "#") {
							channelName = channelName[1:]
						}
						if s.RoomExists(channelName) {
							channelsToList = append(channelsToList, channelName)
						}
					}
				}

				// Send RPL_LIST for each channel
				for _, channelName := range channelsToList {
					s.RoomsMutex.Lock()
					var room *Room
					for i := range s.Rooms {
						if s.Rooms[i].name == channelName {
							room = &s.Rooms[i]
							break
						}
					}
					s.RoomsMutex.Unlock()

					if room != nil {
						room.roomMux.Lock()
						visibleCount := len(room.clients)
						topic := room.topic
						room.roomMux.Unlock()

						if topic == "" {
							topic = "No topic is set"
						}

						// Format: :server 322 <nick> <channel> <# visible> :<topic>
						send(conn, ":%s %s %s #%s %d :%s\n",
							config.Values.Server.Host, RplList, client.nick,
							channelName, visibleCount, topic)
					}
				}

				// Send RPL_LISTEND
				send(conn, ":%s %s %s :End of LIST\n",
					config.Values.Server.Host, RplListEnd, client.nick)

				log.Infof("[LIST] Sent %d channels to %s", len(channelsToList), client.nick)
			} else if cmd == TopicCmd && initCompleted {
				// Command: TOPIC <channel> [<topic>]
				// Query or set channel topic
				if len(args) == 0 {
					send(conn, ":%s %s %s :Not enough parameters\n", config.Values.Server.Host, ErrNeedMoreParams, TopicCmd)
					continue
				}
				parts := strings.SplitN(args, ":", 2)
				channelName := strings.TrimSpace(parts[0])

				// Check if channel exists
				if !s.RoomExists(channelName) {
					send(conn, ":%s %s %s :No such channel\n", config.Values.Server.Host, ErrNoSuchChannel, channelName)
					continue
				}

				// Check if user is in the channel
				inChannel, _ := s.IsUserInRoom(client, channelName)
				if !inChannel {
					send(conn, ":%s %s %s :You're not on that channel\n", config.Values.Server.Host, ErrNotOnChannel, channelName)
					continue
				}

				// Get the actual room pointer from server's slice
				room := s.GetRoom(channelName)
				if room == nil {
					send(conn, ":%s %s %s :No such channel\n", config.Values.Server.Host, ErrNoSuchChannel, channelName)
					continue
				}

				// Query topic (no second parameter)
				if len(parts) == 1 {
					room.roomMux.Lock()
					topic := room.topic
					room.roomMux.Unlock()

					if topic == "" {
						// RPL_NOTOPIC: 331 <client> <channel> :No topic is set
						send(conn, ":%s %s %s %s :No topic is set\n",
							config.Values.Server.Host, RplNoTopic, client.nick, channelName)
					} else {
						// RPL_TOPIC: 332 <client> <channel> :<topic>
						send(conn, ":%s %s %s %s :%s\n",
							config.Values.Server.Host, RplTopic, client.nick, channelName, topic)
					}
					log.Infof("[TOPIC] %s queried topic for %s", client.nick, channelName)
				} else {
					// Set topic (has second parameter)
					newTopic := parts[1]

					// Check if channel has +t mode (topic lock) - only operators/founders can set
					room.roomMux.Lock()
					topicLocked := room.topicLock
					isOp := false
					isFounder := false
					if room.operators != nil {
						_, isOp = room.operators[client.identifier]
					}
					if room.founders != nil {
						_, isFounder = room.founders[client.identifier]
					}
					room.roomMux.Unlock()

					if topicLocked && !isOp && !isFounder {
						send(conn, ":%s %s %s :You're not channel operator\n",
							config.Values.Server.Host, ErrChanOpPrivsNeeded, channelName)
						log.Infof("[TOPIC] %s denied setting topic for %s (not operator/founder)", client.nick, channelName)
						continue
					}

					// Set the topic
					room.roomMux.Lock()
					room.topic = newTopic
					room.roomMux.Unlock()

					// Notify all users in the channel about the topic change
					// Format: :nick!user@host TOPIC #channel :new topic
					room.roomMux.Lock()
					clientIDs := make([]uuid.UUID, 0, len(room.clients))
					for ident := range room.clients {
						clientIDs = append(clientIDs, ident)
					}
					room.roomMux.Unlock()

					for _, ident := range clientIDs {
						s.ClientsMutex.Lock()
						for i := range s.Clients {
							if s.Clients[i].identifier == ident {
								send(s.Clients[i].conn, ":%s!%s@%s TOPIC %s :%s\n",
									client.nick, client.user, config.Values.Server.Host,
									channelName, newTopic)
								break
							}
						}
						s.ClientsMutex.Unlock()
					}

					log.Infof("[TOPIC] %s set topic for %s: %s", client.nick, channelName, newTopic)
				}
			} else if cmd == OperCmd && initCompleted {
				// Command: OPER <username> <password>
				// Authenticate as IRC operator
				if len(args) == 0 {
					send(conn, ":%s %s %s :Not enough parameters\n", config.Values.Server.Host, ErrNeedMoreParams, OperCmd)
					continue
				}

				// Parse username and password
				parts := strings.Fields(args)
				if len(parts) < 2 {
					send(conn, ":%s %s %s :Not enough parameters\n", config.Values.Server.Host, ErrNeedMoreParams, OperCmd)
					log.Infof("[OPER] %s failed: insufficient parameters", client.nick)
					continue
				}

				username := parts[0]
				password := parts[1]

				log.Infof("[OPER] %s attempting to authenticate as '%s'", client.nick, username)

				// Load users from config
				users, err := config.LoadUsers()
				if err != nil {
					send(conn, ":%s %s :Configuration error\n", config.Values.Server.Host, ErrPasswdMismatch)
					log.Errorf("[OPER] Failed to load users: %v", err)
					continue
				}

				// Authenticate user
				user, authenticated := config.AuthenticateUser(username, password, users)
				if !authenticated {
					// ERR_PASSWDMISMATCH: 464 :Password incorrect
					send(conn, ":%s %s :Password incorrect\n", config.Values.Server.Host, ErrPasswdMismatch)
					log.Infof("[OPER] %s failed: invalid credentials for '%s'", client.nick, username)
					continue
				}

				// Check if user has operator privileges
				if !user.IsOper {
					// ERR_NOOPERHOST: 491 :No O-lines for your host
					send(conn, ":%s %s :No O-lines for your host\n", config.Values.Server.Host, ErrNoOperHost)
					log.Infof("[OPER] %s failed: '%s' is not authorized as operator", client.nick, username)
					continue
				}

				// Grant operator status
				client.clientMux.Lock()
				client.operator = true
				client.accountName = username
				client.clientMux.Unlock()

				// Update the client in the server's client list
				s.ClientsMutex.Lock()
				for i := range s.Clients {
					if s.Clients[i].identifier == client.identifier {
						s.Clients[i].operator = true
						s.Clients[i].accountName = username
						break
					}
				}
				s.ClientsMutex.Unlock()

				// Make the first operator to log in a founder of #lobby
				s.RoomsMutex.Lock()
				for i := range s.Rooms {
					if s.Rooms[i].name == "lobby" {
						s.Rooms[i].roomMux.Lock()
						// Only make them founder if there are no founders yet
						if len(s.Rooms[i].founders) == 0 {
							s.Rooms[i].founders[client.identifier] = true
							log.Infof("[OPER] %s is the first operator and is now founder of #lobby", client.nick)
						}
						s.Rooms[i].roomMux.Unlock()
						break
					}
				}
				s.RoomsMutex.Unlock()

				// RPL_YOUREOPER: 381 :You are now an IRC operator
				send(conn, ":%s %s %s :You are now an IRC operator\n",
					config.Values.Server.Host, RplYoureOper, client.nick)
				log.Infof("[OPER] %s successfully authenticated as operator '%s'", client.nick, username)
			} else if cmd == NoticeCmd && initCompleted {
				// Command: NOTICE <target> :<message>
				// Similar to PRIVMSG but must not trigger automatic replies
				if len(args) == 0 {
					// NOTICE should fail silently - no error responses per RFC
					log.Infof("[NOTICE] No recipient given, failing silently")
					continue
				} else {
					parts := strings.Split(args, ":")
					if len(parts) < 2 {
						// NOTICE should fail silently
						log.Infof("[NOTICE] No text to send, failing silently")
						continue
					}

					target := strings.TrimSpace(parts[0])
					noticeMessage := strings.TrimSpace(parts[1])

					if len(target) == 0 || len(noticeMessage) == 0 {
						// NOTICE should fail silently
						log.Infof("[NOTICE] Empty target or message, failing silently")
						continue
					}

					if strings.HasPrefix(target, "#") || strings.HasPrefix(target, "$") {
						// Channel or mask target
						if validMessageMask(target) {
							// Masking is not implemented
							log.Infof("[NOTICE] Masking not implemented, failing silently")
							continue
						} else {
							// Send to channel
							if inChannel, room := s.IsUserInRoom(client, target); inChannel {
								room.roomMux.Lock()
								for ident := range room.clients {
									if ident == client.identifier {
										continue // Don't send to sender
									}
									for _, cli := range s.Clients {
										if ident == cli.identifier {
											send(cli.conn, ":%s!%s@%s %s %s :%s\n",
												client.nick, client.user, config.Values.Server.Host,
												NoticeCmd, target, noticeMessage)
										}
									}
								}
								room.roomMux.Unlock()
								log.Infof("[NOTICE] %s sent notice to channel %s", client.nick, target)
							} else {
								// NOTICE should fail silently - user not in channel
								log.Infof("[NOTICE] %s not in channel %s, failing silently", client.nick, target)
							}
						}
					} else if validNickCharset(target) {
						// User target
						targetClient := s.ClientByName(target)
						if targetClient.nick == "" {
							// NOTICE should fail silently - no such user
							log.Infof("[NOTICE] Target user %s not found, failing silently", target)
						} else {
							send(targetClient.conn, ":%s!%s@%s %s %s :%s\n",
								client.nick, client.user, config.Values.Server.Host,
								NoticeCmd, target, noticeMessage)
							log.Infof("[NOTICE] %s sent notice to %s", client.nick, target)
						}
					}
				}
			} else if cmd == KillCmd && initCompleted {
				// Command: KILL <nickname> :<reason>
				// Operator command to forcefully disconnect a user
				log.Infof("[KILL] %s attempting KILL: '%s'", client.nick, args)

				// Check if user is operator
				if !client.operator {
					send(conn, ":%s %s :Permission denied- You're not an IRC operator\n",
						config.Values.Server.Host, ErrNoPrivileges)
					log.Infof("[KILL] %s denied - not an operator", client.nick)
					continue
				}

				// Parse arguments
				parts := strings.SplitN(args, ":", 2)
				if len(parts) < 2 {
					send(conn, ":%s %s %s :Not enough parameters\n",
						config.Values.Server.Host, ErrNeedMoreParams, KillCmd)
					continue
				}

				targetNick := strings.TrimSpace(parts[0])
				reason := strings.TrimSpace(parts[1])

				if len(targetNick) == 0 {
					send(conn, ":%s %s * :No such nick/channel\n",
						config.Values.Server.Host, ErrNoSuchNick)
					continue
				}

				// Find target client
				s.ClientsMutex.Lock()
				var targetClient *Client
				for i := range s.Clients {
					if s.Clients[i].nick == targetNick {
						targetClient = &s.Clients[i]
						break
					}
				}
				s.ClientsMutex.Unlock()

				if targetClient == nil || targetClient.nick == "" {
					send(conn, ":%s %s %s :No such nick/channel\n",
						config.Values.Server.Host, ErrNoSuchNick, targetNick)
					log.Infof("[KILL] Target %s not found", targetNick)
					continue
				}

				// Build KILL message and QUIT notification
				killMsg := fmt.Sprintf("Killed by %s (%s)", client.nick, reason)

				// Send QUIT notification to all users in the same channels
				// This lets other users know this client has been killed
				if targetClient.nick != "" {
					quitMsg := fmt.Sprintf(":%s!%s@%s QUIT :Killed: %s\r\n",
						targetClient.nick, targetClient.user, config.Values.Server.Host, killMsg)

					// Collect all unique clients in the same rooms
					notifiedClients := make(map[uuid.UUID]bool)

					for roomID := range targetClient.rooms {
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
							for clientID := range room.clients {
								if clientID != targetClient.identifier && !notifiedClients[clientID] {
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

					// Broadcast to Redis if enabled
					if s.RedisClient != nil {
						ctx := context.Background()
						eventData := map[string]interface{}{
							"uuid":   targetClient.identifier.String(),
							"nick":   targetClient.nick,
							"reason": killMsg,
						}
						event := redis.Event{
						Type: "QUIT",
						Data: eventData,
					}
					if err := s.RedisClient.PublishEvent(ctx, event); err != nil {
							log.Errorf("[KILL] Failed to publish QUIT event to Redis: %v", err)
						}
					}

					log.Infof("[KILL] %s killed %s: %s", client.nick, targetNick, reason)
				}

				// Send ERROR to target
				send(targetClient.conn, "ERROR :Closing Link: %s (Killed (%s (%s)))\r\n",
					targetNick, client.nick, reason)

				// Disconnect target
				if targetClient.conn != nil {
					targetClient.conn.Close()
				}

				// Remove client from server
				s.RemoveClient(targetClient.identifier)

			} else if cmd == WallopsCmd && initCompleted {
				// Command: WALLOPS :<message>
				// Send message to all users with +w (wallops) mode set
				log.Infof("[WALLOPS] %s sending wallops: '%s'", client.nick, args)

				// Check if user is operator
				if !client.operator {
					send(conn, ":%s %s :Permission denied- You're not an IRC operator\n",
						config.Values.Server.Host, ErrNoPrivileges)
					log.Infof("[WALLOPS] %s denied - not an operator", client.nick)
					continue
				}

				// Extract message (should start with :)
				var message string
				if strings.HasPrefix(args, ":") {
					message = strings.TrimPrefix(args, ":")
				} else {
					message = args
				}

				if len(message) == 0 {
					send(conn, ":%s %s %s :Not enough parameters\n",
						config.Values.Server.Host, ErrNeedMoreParams, WallopsCmd)
					continue
				}

				// Send to all users with +w mode
				wallopsMsg := fmt.Sprintf(":%s!%s@%s WALLOPS :%s\r\n",
					client.nick, client.user, config.Values.Server.Host, message)

				s.ClientsMutex.Lock()
				count := 0
				for i := range s.Clients {
					// Send to users with wallops mode (+w) OR operators
					if s.Clients[i].wallops || s.Clients[i].operator {
						if s.Clients[i].conn != nil {
							send(s.Clients[i].conn, "%s", wallopsMsg)
							count++
						}
					}
				}
				s.ClientsMutex.Unlock()

				// Broadcast to Redis if enabled
				if s.RedisClient != nil {
					ctx := context.Background()
					eventData := map[string]interface{}{
						"from_nick": client.nick,
						"message":   message,
					}
					event := redis.Event{
						Type: "WALLOPS",
						Data: eventData,
					}
					if err := s.RedisClient.PublishEvent(ctx, event); err != nil {
						log.Errorf("[WALLOPS] Failed to publish event to Redis: %v", err)
					}
				}

				log.Infof("[WALLOPS] %s sent wallops to %d users", client.nick, count)

			} else if cmd == ChanRegCmd && initCompleted {
				// Command: CHANREG <#channel> [:<description>]
				// Register a channel and become its founder
				log.Infof("[CHANREG] %s attempting to register channel: '%s'", client.nick, args)

				// Must be authenticated
				if !client.authenticated || client.accountName == "" {
					send(conn, ":%s %s :You must be logged in to register a channel\n",
						config.Values.Server.Host, ErrAccountRequired)
					log.Infof("[CHANREG] %s denied - not authenticated", client.nick)
					continue
				}

				// Parse arguments
				var channelName, description string
				if strings.HasPrefix(args, "#") {
					parts := strings.SplitN(args, " :", 2)
					channelName = strings.TrimSpace(parts[0])
					if len(parts) > 1 {
						description = strings.TrimSpace(parts[1])
					}
				} else {
					send(conn, ":%s %s %s :Not enough parameters\n",
						config.Values.Server.Host, ErrNeedMoreParams, ChanRegCmd)
					continue
				}

				// Validate channel name
				if !strings.HasPrefix(channelName, "#") || len(channelName) < 2 {
					send(conn, ":%s %s %s :No such channel\n",
						config.Values.Server.Host, ErrNoSuchChannel, channelName)
					continue
				}

				// Check if user is in the channel
				inChannel, room := s.IsUserInRoom(client, channelName)
				if !inChannel {
					send(conn, ":%s %s %s :You're not on that channel\n",
						config.Values.Server.Host, ErrNotOnChannel, channelName)
					log.Infof("[CHANREG] %s denied - not in channel %s", client.nick, channelName)
					continue
				}

				// Check if user is channel operator
				room.roomMux.Lock()
				isOp := room.operators[client.identifier]
				isFounder := room.founders[client.identifier]
				room.roomMux.Unlock()

				if !isOp && !isFounder {
					send(conn, ":%s %s %s :You're not channel operator\n",
						config.Values.Server.Host, ErrChanOpPrivsNeeded, channelName)
					log.Infof("[CHANREG] %s denied - not operator in %s", client.nick, channelName)
					continue
				}

				// Check if channel is already registered
				if s.RedisClient != nil {
					ctx := context.Background()
					isRegistered, err := s.RedisClient.IsChannelRegistered(ctx, channelName)
					if err != nil {
						log.Errorf("[CHANREG] Error checking registration status: %v", err)
					}
					if isRegistered {
						send(conn, ":%s %s %s :Channel already registered\n",
							config.Values.Server.Host, ErrChanRegisteredAlready, channelName)
						log.Infof("[CHANREG] %s denied - %s already registered", client.nick, channelName)
						continue
					}

					// Register the channel
					err = s.RedisClient.RegisterChannel(ctx, channelName, client.accountName, description)
					if err != nil {
						send(conn, ":%s %s :Failed to register channel\n",
							config.Values.Server.Host, ErrNoSuchService)
						log.Errorf("[CHANREG] Failed to register %s: %v", channelName, err)
						continue
					}

					// Mark channel as registered and add user as founder
					room.roomMux.Lock()
					room.registered = true
					room.founders[client.identifier] = true
					room.roomMux.Unlock()

					// Success!
					send(conn, ":%s %s %s :Channel registered successfully\n",
						config.Values.Server.Host, RplChanRegistered, channelName)
					log.Infof("[CHANREG] %s successfully registered %s as founder %s",
						client.nick, channelName, client.accountName)
				} else {
					// No Redis - cannot register channels
					send(conn, ":%s %s :Channel registration requires Redis\n",
						config.Values.Server.Host, ErrNoSuchService)
					log.Infof("[CHANREG] %s denied - Redis not available", client.nick)
				}

			} else if cmd == "NAMES" && initCompleted {
				// Command: NAMES [<channel>{,<channel>}]
				// Lists all visible users in specified channels or all channels
				log.Infof("[NAMES] %s requested names: '%s'", client.nick, args)

				var channelsToList []string

				if len(args) == 0 {
					// List all channels the user is in
					s.RoomsMutex.Lock()
					for roomID := range client.rooms {
						for i := range s.Rooms {
							if s.Rooms[i].identifier == roomID {
								channelsToList = append(channelsToList, s.Rooms[i].name)
								break
							}
						}
					}
					s.RoomsMutex.Unlock()
				} else {
					// List specific channels (comma-separated)
					channels := strings.Split(args, ",")
					for _, ch := range channels {
						channelName := strings.TrimSpace(ch)
						if strings.HasPrefix(channelName, "#") {
							channelName = channelName[1:]
						}
						if s.RoomExists(channelName) {
							channelsToList = append(channelsToList, channelName)
						}
					}
				}

				// Send RPL_NAMREPLY for each channel
				for _, channelName := range channelsToList {
					s.RoomsMutex.Lock()
					var room *Room
					for i := range s.Rooms {
						if s.Rooms[i].name == channelName {
							room = &s.Rooms[i]
							break
						}
					}
					s.RoomsMutex.Unlock()

					if room != nil {
						// Check if user is in the channel (for visibility)
						inChannel, _ := s.IsUserInRoom(client, channelName)
						if !inChannel {
							// User not in channel - skip or send empty list based on channel modes
							// For now, skip channels user is not in
							continue
						}

						// Collect all nicknames in the channel
						room.roomMux.Lock()
						var names []string
						for clientID := range room.clients {
							s.ClientsMutex.Lock()
							for i := range s.Clients {
								if s.Clients[i].identifier == clientID {
									nick := s.Clients[i].nick
									// Check if user is operator in this channel
									if room.operators != nil {
										if _, isOp := room.operators[clientID]; isOp {
											nick = "@" + nick
										}
									}
									names = append(names, nick)
									break
								}
							}
							s.ClientsMutex.Unlock()
						}
						room.roomMux.Unlock()

						// Send RPL_NAMREPLY: 353 <client> = <channel> :<names>
						if len(names) > 0 {
							send(conn, ":%s %s %s = #%s :%s\n",
								config.Values.Server.Host, RplNameReply, client.nick,
								channelName, strings.Join(names, " "))
						}
					}
				}

				// Send RPL_ENDOFNAMES: 366 <client> <channel> :End of NAMES list
				if len(channelsToList) > 0 {
					for _, channelName := range channelsToList {
						send(conn, ":%s %s %s #%s :End of NAMES list\n",
							config.Values.Server.Host, RplEndOfNames, client.nick, channelName)
					}
				} else {
					// No channels specified or user not in any channels
					send(conn, ":%s %s %s * :End of NAMES list\n",
						config.Values.Server.Host, RplEndOfNames, client.nick)
				}

				log.Infof("[NAMES] Sent names for %d channels to %s", len(channelsToList), client.nick)
			} else if cmd == WhoCmd && initCompleted {
				// Command: WHO [<mask>]
				// Query information about users matching the mask
				// If mask is a channel (starts with #), list users in that channel
				// If mask is a nickname pattern, list matching users
				// If no mask, list all visible users
				log.Infof("[WHO] %s requested WHO: '%s'", client.nick, args)

				var usersToList []Client
				mask := strings.TrimSpace(args)

				if mask == "" {
					// No mask - list all visible users (users in same channels as requester)
					s.ClientsMutex.Lock()
					for _, cli := range s.Clients {
						usersToList = append(usersToList, cli)
					}
					s.ClientsMutex.Unlock()
				} else if strings.HasPrefix(mask, "#") {
					// Channel mask - list users in the channel
					channelName := mask[1:]
					if s.RoomExists(channelName) {
						// Check if requester is in channel (for visibility)
						inChannel, room := s.IsUserInRoom(client, channelName)
						if inChannel {
							// Get all users in channel
							room.roomMux.Lock()
							for clientID := range room.clients {
								s.ClientsMutex.Lock()
								for i := range s.Clients {
									if s.Clients[i].identifier == clientID {
										usersToList = append(usersToList, s.Clients[i])
										break
									}
								}
								s.ClientsMutex.Unlock()
							}
							room.roomMux.Unlock()
						}
					}
				} else {
					// Nickname pattern mask - list matching users
					s.ClientsMutex.Lock()
					for _, cli := range s.Clients {
						// Simple pattern matching (just check if nick matches)
						if strings.Contains(strings.ToLower(cli.nick), strings.ToLower(mask)) {
							usersToList = append(usersToList, cli)
						}
					}
					s.ClientsMutex.Unlock()
				}

				// Send RPL_WHOREPLY for each user
				// Format: 352 <client> <channel> <user> <host> <server> <nick> <H|G>[*][@|+] :<hopcount> <real name>
				for _, user := range usersToList {
					// Find which channel to report (use first common channel, or * if none)
					channelName := "*"
					// Check if user is away: H = here, G = gone (away)
					flags := "H"
					if user.away {
						flags = "G"
					}

					// Find a common channel between requester and target user
					if mask != "" && strings.HasPrefix(mask, "#") {
						// If mask was a channel, use that
						channelName = mask
					} else {
						// Find first common channel
						s.RoomsMutex.Lock()
						for roomID := range client.rooms {
							if user.rooms[roomID] {
								// Found common room
								for i := range s.Rooms {
									if s.Rooms[i].identifier == roomID {
										channelName = "#" + s.Rooms[i].name
										break
									}
								}
								break
							}
						}
						s.RoomsMutex.Unlock()
					}

					// Check if user is operator in the channel
					if channelName != "*" {
						roomName := strings.TrimPrefix(channelName, "#")
						s.RoomsMutex.Lock()
						for i := range s.Rooms {
							if s.Rooms[i].name == roomName {
								s.Rooms[i].roomMux.Lock()
								if s.Rooms[i].operators != nil {
									if _, isOp := s.Rooms[i].operators[user.identifier]; isOp {
										flags += "@"
									}
								}
								s.Rooms[i].roomMux.Unlock()
								break
							}
						}
						s.RoomsMutex.Unlock()
					}

					// RPL_WHOREPLY: 352 <client> <channel> <user> <host> <server> <nick> <H|G>[*][@|+] :<hopcount> <real name>
					send(conn, ":%s %s %s %s %s %s %s %s %s :0 %s\n",
						config.Values.Server.Host, RplWhoReply, client.nick,
						channelName, user.user, config.Values.Server.Host,
						config.Values.Server.Host, user.nick, flags, user.realname)
				}

				// Send RPL_ENDOFWHO: 315 <client> <name> :End of WHO list
				endMask := mask
				if endMask == "" {
					endMask = "*"
				}
				send(conn, ":%s %s %s %s :End of WHO list\n",
					config.Values.Server.Host, RplEndOfWho, client.nick, endMask)

				log.Infof("[WHO] Sent WHO reply for %d users to %s", len(usersToList), client.nick)
			} else if cmd == AwayCmd && initCompleted {
				// Command: AWAY [:<message>]
				// Set or unset away status
				// AWAY with no args = unset away
				// AWAY :<message> = set away with message
				message := strings.TrimSpace(args)

				s.ClientsMutex.Lock()
				for i := range s.Clients {
					if s.Clients[i].identifier == client.identifier {
						if message == "" {
							// Unset away
							s.Clients[i].away = false
							s.Clients[i].awayMessage = ""

							// RPL_UNAWAY: 305 <client> :You are no longer marked as being away
							send(conn, ":%s %s %s :You are no longer marked as being away\n",
								config.Values.Server.Host, RplUnAway, client.nick)

							log.Infof("[AWAY] %s is no longer away", client.nick)
						} else {
							// Set away
							s.Clients[i].away = true
							s.Clients[i].awayMessage = message

							// RPL_NOWAWAY: 306 <client> :You have been marked as being away
							send(conn, ":%s %s %s :You have been marked as being away\n",
								config.Values.Server.Host, RplNowAway, client.nick)

							log.Infof("[AWAY] %s is now away: %s", client.nick, message)
						}
						break
					}
				}
				s.ClientsMutex.Unlock()
			} else if cmd == VersionCmd && initCompleted {
				// Command: VERSION [<target>]
				// Returns the version of the server program
				// For now we only support VERSION without target (query this server)

				// RFC 1459: RPL_VERSION "351"
				// Format: 351 <client> <version>.<debuglevel> <server> :<comments>
				versionStr := version.GetVersion()
				debugLevel := "0"
				serverName := config.Values.Server.Host
				comments := "IRC Server written in Go"

				send(conn, ":%s %s %s %s.%s %s :%s\n",
					config.Values.Server.Host, RplVersion, client.nick,
					versionStr, debugLevel, serverName, comments)

				log.Infof("[VERSION] %s requested server version: %s", client.nick, versionStr)
			} else if cmd == MotdCmd && initCompleted {
				// Command: MOTD [<target>]
				// Returns the Message of the Day
				// For now we only support MOTD without target (query this server)

				// RFC 1459: RPL_MOTDSTART "375", RPL_MOTD "372", RPL_ENDOFMOTD "376"
				serverName := config.Values.Server.Host

				// Send MOTD start
				send(conn, ":%s %s %s :- %s Message of the day -\n",
					serverName, RplMotdStart, client.nick, serverName)

				// Send MOTD lines
				motdLines := []string{
					"Welcome to girc - IRC Server written in Go",
					"",
					"This is a simple IRC server implementation",
					"following RFC 1459 specifications.",
					"",
					"For more information, visit:",
					"https://github.com/tehcyx/girc",
				}

				for _, line := range motdLines {
					send(conn, ":%s %s %s :- %s\n",
						serverName, RplMotd, client.nick, line)
				}

				// Send MOTD end
				send(conn, ":%s %s %s :End of MOTD command\n",
					serverName, RplEndOfMotd, client.nick)

				log.Infof("[MOTD] %s requested MOTD", client.nick)
			} else if cmd == IsonCmd && initCompleted {
				// Command: ISON <nickname> [<nickname> ...]
				// Check which of the specified nicknames are currently online
				// Returns a space-separated list of online nicknames

				// RFC 1459: RPL_ISON "303"
				nicknames := strings.Fields(args)
				var onlineNicks []string

				if len(nicknames) == 0 {
					// If no nicknames provided, return empty ISON reply
					send(conn, ":%s %s %s :\n",
						config.Values.Server.Host, RplIson, client.nick)
					continue
				}

				// Check each nickname against connected clients
				s.ClientsMutex.Lock()
				for _, requestedNick := range nicknames {
					for _, cli := range s.Clients {
						if strings.EqualFold(cli.nick, requestedNick) {
							onlineNicks = append(onlineNicks, cli.nick)
							break
						}
					}
				}
				s.ClientsMutex.Unlock()

				// Send RPL_ISON with space-separated list of online nicks
				onlineList := strings.Join(onlineNicks, " ")
				send(conn, ":%s %s %s :%s\n",
					config.Values.Server.Host, RplIson, client.nick, onlineList)

				log.Infof("[ISON] %s checked %d nicknames, %d online",
					client.nick, len(nicknames), len(onlineNicks))
			} else if cmd == UserhostCmd && initCompleted {
				// Command: USERHOST <nickname> [<nickname> ...]
				// Returns information about users including away status and operator status
				// Format: nick[*]=<+|-><user>@<host>
				// * indicates operator, + indicates here, - indicates away

				// RFC 1459: RPL_USERHOST "302"
				nicknames := strings.Fields(args)
				var replies []string

				if len(nicknames) == 0 {
					// If no nicknames provided, return empty USERHOST reply
					send(conn, ":%s %s %s :\n",
						config.Values.Server.Host, RplUserhost, client.nick)
					continue
				}

				// RFC 1459 limits USERHOST to 5 nicknames
				if len(nicknames) > 5 {
					nicknames = nicknames[:5]
				}

				// Build replies for each found user
				s.ClientsMutex.Lock()
				for _, requestedNick := range nicknames {
					for _, cli := range s.Clients {
						if strings.EqualFold(cli.nick, requestedNick) {
							// Format: nick[*]=<+|-><user>@<host>
							operatorFlag := ""
							if cli.operator {
								operatorFlag = "*"
							}

							awayFlag := "+"
							if cli.away {
								awayFlag = "-"
							}

							// Get hostname from connection
							host := "localhost"
							if cli.conn != nil {
								if addr := cli.conn.RemoteAddr(); addr != nil {
									host = strings.Split(addr.String(), ":")[0]
								}
							}

							reply := fmt.Sprintf("%s%s=%s%s@%s",
								cli.nick, operatorFlag, awayFlag, cli.user, host)
							replies = append(replies, reply)
							break
						}
					}
				}
				s.ClientsMutex.Unlock()

				// Send RPL_USERHOST with space-separated list of replies
				replyList := strings.Join(replies, " ")
				send(conn, ":%s %s %s :%s\n",
					config.Values.Server.Host, RplUserhost, client.nick, replyList)

				log.Infof("[USERHOST] %s queried %d nicknames, %d found",
					client.nick, len(nicknames), len(replies))
			} else if cmd == ModeCmd && initCompleted {
				// Command: MODE <target> [<modestring> [<mode arguments>...]]
				// Two types: user modes and channel modes
				// User mode: MODE nickname +/-flags
				// Channel mode: MODE #channel +/-flags [parameters]

				parts := strings.Fields(args)
				if len(parts) == 0 {
					send(conn, ":%s %s %s :Not enough parameters\n",
						config.Values.Server.Host, ErrNeedMoreParams, ModeCmd)
					continue
				}

				target := parts[0]

				// Check if target is a channel (starts with #)
				if strings.HasPrefix(target, "#") {
					// Channel mode
					if len(parts) == 1 {
						// Query channel modes
						// Strip # prefix from target for comparison with room names
						channelName := strings.TrimPrefix(target, "#")

						s.RoomsMutex.Lock()
						var room *Room
						for i := range s.Rooms {
							if strings.EqualFold(s.Rooms[i].name, channelName) {
								room = &s.Rooms[i]
								break
							}
						}
						s.RoomsMutex.Unlock()

						if room == nil {
							send(conn, ":%s %s %s %s :No such channel\n",
								config.Values.Server.Host, ErrNoSuchChannel, client.nick, target)
							continue
						}

						// Build mode string
						var modeStr strings.Builder
						modeStr.WriteString("+")
						if room.topicLock {
							modeStr.WriteString("t")
						}
						if room.noExternal {
							modeStr.WriteString("n")
						}
						if room.moderated {
							modeStr.WriteString("m")
						}
						if room.inviteOnly {
							modeStr.WriteString("i")
						}
						if room.secret {
							modeStr.WriteString("s")
						}
						if room.private {
							modeStr.WriteString("p")
						}

						send(conn, ":%s %s %s %s %s\n",
							config.Values.Server.Host, RplChannelModeIs, client.nick, room.name, modeStr.String())
						log.Infof("[MODE] %s queried modes for %s: %s", client.nick, room.name, modeStr.String())
					} else {
						// Set channel modes
						modeString := parts[1]
						modeArgs := parts[2:]

						// Strip # prefix from target for comparison with room names
						channelName := strings.TrimPrefix(target, "#")

						s.RoomsMutex.Lock()
						var room *Room
						var roomIndex int
						for i := range s.Rooms {
							if strings.EqualFold(s.Rooms[i].name, channelName) {
								room = &s.Rooms[i]
								roomIndex = i
								break
							}
						}

						if room == nil {
							s.RoomsMutex.Unlock()
							send(conn, ":%s %s %s %s :No such channel\n",
								config.Values.Server.Host, ErrNoSuchChannel, client.nick, target)
							continue
						}

						// Check if user is channel operator or founder
						if !room.operators[client.identifier] && !room.founders[client.identifier] {
							s.RoomsMutex.Unlock()
							send(conn, ":%s %s %s %s :You're not channel operator\n",
								config.Values.Server.Host, ErrChanOpPrivsNeeded, client.nick, target)
							continue
						}

						// Parse and apply mode changes
						adding := true
						argIndex := 0
						var appliedModes strings.Builder
						var appliedArgs []string

						for _, ch := range modeString {
							switch ch {
							case '+':
								adding = true
								if appliedModes.Len() == 0 || appliedModes.String()[appliedModes.Len()-1] != '+' {
									appliedModes.WriteRune('+')
								}
							case '-':
								adding = false
								if appliedModes.Len() == 0 || appliedModes.String()[appliedModes.Len()-1] != '-' {
									appliedModes.WriteRune('-')
								}
							case 't': // Topic lock
								s.Rooms[roomIndex].topicLock = adding
								appliedModes.WriteRune('t')
							case 'n': // No external messages
								s.Rooms[roomIndex].noExternal = adding
								appliedModes.WriteRune('n')
							case 'm': // Moderated
								s.Rooms[roomIndex].moderated = adding
								appliedModes.WriteRune('m')
							case 'i': // Invite only
								s.Rooms[roomIndex].inviteOnly = adding
								appliedModes.WriteRune('i')
							case 's': // Secret
								s.Rooms[roomIndex].secret = adding
								appliedModes.WriteRune('s')
							case 'p': // Private
								s.Rooms[roomIndex].private = adding
								appliedModes.WriteRune('p')
							case 'o': // Operator
								if argIndex < len(modeArgs) {
									targetNick := modeArgs[argIndex]
									argIndex++

									// Find target client
									s.ClientsMutex.Lock()
									var targetClient *Client
									for i := range s.Clients {
										if strings.EqualFold(s.Clients[i].nick, targetNick) {
											targetClient = &s.Clients[i]
											break
										}
									}
									s.ClientsMutex.Unlock()

									if targetClient != nil && room.clients[targetClient.identifier] {
										if adding {
											s.Rooms[roomIndex].operators[targetClient.identifier] = true
										} else {
											delete(s.Rooms[roomIndex].operators, targetClient.identifier)
										}
										appliedModes.WriteRune('o')
										appliedArgs = append(appliedArgs, targetNick)
									}
								}
							case 'v': // Voice
								if argIndex < len(modeArgs) {
									targetNick := modeArgs[argIndex]
									argIndex++

									// Find target client
									s.ClientsMutex.Lock()
									var targetClient *Client
									for i := range s.Clients {
										if strings.EqualFold(s.Clients[i].nick, targetNick) {
											targetClient = &s.Clients[i]
											break
										}
									}
									s.ClientsMutex.Unlock()

									if targetClient != nil && room.clients[targetClient.identifier] {
										if adding {
											s.Rooms[roomIndex].voiced[targetClient.identifier] = true
										} else {
											delete(s.Rooms[roomIndex].voiced, targetClient.identifier)
										}
										appliedModes.WriteRune('v')
										appliedArgs = append(appliedArgs, targetNick)
									}
								}
							}
						}
						s.RoomsMutex.Unlock()

						// Broadcast mode change to all channel members
						if appliedModes.Len() > 0 {
							modeChange := appliedModes.String()
							modeArgsStr := ""
							if len(appliedArgs) > 0 {
								modeArgsStr = " " + strings.Join(appliedArgs, " ")
							}

							s.ClientsMutex.Lock()
							for i := range s.Clients {
								if room.clients[s.Clients[i].identifier] {
									send(s.Clients[i].conn, ":%s!%s@%s MODE %s %s%s\n",
										client.nick, client.user, "localhost", room.name, modeChange, modeArgsStr)
								}
							}
							s.ClientsMutex.Unlock()

							log.Infof("[MODE] %s set modes on %s: %s%s", client.nick, room.name, modeChange, modeArgsStr)
						}
					}
				} else {
					// User mode
					if len(parts) == 1 {
						// Query user modes (only own modes)
						if !strings.EqualFold(target, client.nick) {
							send(conn, ":%s %s %s :Cannot view modes for other users\n",
								config.Values.Server.Host, ErrUsersDontMatch, client.nick)
							continue
						}

						// Build user mode string
						var modeStr strings.Builder
						modeStr.WriteString("+")
						if client.invisible {
							modeStr.WriteString("i")
						}
						if client.operator {
							modeStr.WriteString("o")
						}
						if client.wallops {
							modeStr.WriteString("w")
						}
						if client.notices {
							modeStr.WriteString("s")
						}

						send(conn, ":%s %s %s %s\n",
							config.Values.Server.Host, RplUModeIs, client.nick, modeStr.String())
						log.Infof("[MODE] %s queried own user modes: %s", client.nick, modeStr.String())
					} else {
						// Set user modes (only own modes)
						if !strings.EqualFold(target, client.nick) {
							send(conn, ":%s %s %s :Cannot change mode for other users\n",
								config.Values.Server.Host, ErrUsersDontMatch, client.nick)
							continue
						}

						modeString := parts[1]
						adding := true
						var appliedModes strings.Builder

						s.ClientsMutex.Lock()
						for i := range s.Clients {
							if s.Clients[i].identifier == client.identifier {
								for _, ch := range modeString {
									switch ch {
									case '+':
										adding = true
										if appliedModes.Len() == 0 || appliedModes.String()[appliedModes.Len()-1] != '+' {
											appliedModes.WriteRune('+')
										}
									case '-':
										adding = false
										if appliedModes.Len() == 0 || appliedModes.String()[appliedModes.Len()-1] != '-' {
											appliedModes.WriteRune('-')
										}
									case 'i': // Invisible
										s.Clients[i].invisible = adding
										appliedModes.WriteRune('i')
									case 'w': // Wallops
										s.Clients[i].wallops = adding
										appliedModes.WriteRune('w')
									case 's': // Server notices
										s.Clients[i].notices = adding
										appliedModes.WriteRune('s')
									case 'o': // Operator (cannot be set by user, only removed)
										if !adding && s.Clients[i].operator {
											s.Clients[i].operator = false
											appliedModes.WriteRune('o')
										}
									}
								}
								break
							}
						}
						s.ClientsMutex.Unlock()

						if appliedModes.Len() > 0 {
							send(conn, ":%s MODE %s %s\n", client.nick, client.nick, appliedModes.String())
							log.Infof("[MODE] %s set own user modes: %s", client.nick, appliedModes.String())
						}
					}
				}
			} else if cmd == SetPassCmd && initCompleted {
				// Command: SETPASS <oldpassword> <newpassword>
				// Change password for authenticated account
				log.Infof("Processing SETPASS command")

				if !client.authenticated {
					log.Infof("SETPASS: User '%s' not authenticated", client.nick)
					send(conn, ":%s %s :You must be logged in to change your password\n",
						config.Values.Server.Host, ErrAccountRequired)
					continue
				}

				parts := strings.Fields(args)
				if len(parts) < 2 {
					log.Infof("SETPASS: Not enough parameters")
					send(conn, ":%s %s %s :Not enough parameters (usage: SETPASS <oldpassword> <newpassword>)\n",
						config.Values.Server.Host, ErrNeedMoreParams, SetPassCmd)
					continue
				}

				oldPassword := parts[0]
				newPassword := parts[1]

				log.Infof("SETPASS: User '%s' attempting password change", client.accountName)

				// Validate new password strength
				if !IsValidPassword(newPassword) {
					log.Infof("SETPASS: New password too weak for user '%s'", client.accountName)
					send(conn, ":%s %s :New password does not meet security requirements (minimum 8 characters, maximum 72 characters)\n",
						config.Values.Server.Host, ErrWeakPassword)
					continue
				}

				if s.RedisClient != nil {
					ctx := context.Background()
					// Get user data to verify old password
					userData, err := s.RedisClient.GetUser(ctx, client.accountName)
					if err != nil {
						log.Errorf("SETPASS: Failed to get user data for '%s': %v", client.accountName, err)
						send(conn, ":%s %s :Password change failed - please try again later\n",
							config.Values.Server.Host, ErrNoSuchService)
						continue
					}

					// Verify old password
					if err := VerifyPassword(userData.HashedPassword, oldPassword); err != nil {
						log.Warnf("SETPASS: Old password verification failed for '%s'", client.accountName)
						send(conn, ":%s %s :Old password incorrect\n",
							config.Values.Server.Host, ErrPasswdMismatch)
						continue
					}

					// Hash new password
					newHashedPassword, err := HashPassword(newPassword)
					if err != nil {
						log.Errorf("SETPASS: Failed to hash new password for '%s': %v", client.accountName, err)
						send(conn, ":%s %s :Password change failed - please try again later\n",
							config.Values.Server.Host, ErrNoSuchService)
						continue
					}

					// Update password in Redis
					if err := s.RedisClient.UpdatePassword(ctx, client.accountName, newHashedPassword); err != nil {
						log.Errorf("SETPASS: Failed to update password for '%s': %v", client.accountName, err)
						send(conn, ":%s %s :Password change failed - please try again later\n",
							config.Values.Server.Host, ErrNoSuchService)
						continue
					}

					log.Infof("SETPASS: Successfully changed password for '%s'", client.accountName)
					send(conn, ":%s NOTICE %s :Password changed successfully\n",
						config.Values.Server.Host, client.nick)
				} else {
					log.Warn("SETPASS: Redis not available")
					send(conn, ":%s %s :Account service not available\n",
						config.Values.Server.Host, ErrNoSuchService)
				}
			} else if cmd == WhoAccCmd && initCompleted {
				// Command: WHOACC
				// Display current account information
				log.Infof("Processing WHOACC command for '%s'", client.nick)

				if client.authenticated {
					if s.RedisClient != nil {
						ctx := context.Background()
						userData, err := s.RedisClient.GetUser(ctx, client.accountName)
						if err != nil {
							log.Errorf("WHOACC: Failed to get user data for '%s': %v", client.accountName, err)
							send(conn, ":%s NOTICE %s :Account: %s (error retrieving details)\n",
								config.Values.Server.Host, client.nick, client.accountName)
						} else {
							send(conn, ":%s NOTICE %s :Account: %s\n",
								config.Values.Server.Host, client.nick, userData.Username)
							send(conn, ":%s NOTICE %s :Email: %s\n",
								config.Values.Server.Host, client.nick, userData.Email)
							send(conn, ":%s NOTICE %s :Created: %s\n",
								config.Values.Server.Host, client.nick, userData.CreatedAt.Format("2006-01-02 15:04:05"))
							if !userData.LastLogin.IsZero() {
								send(conn, ":%s NOTICE %s :Last Login: %s\n",
									config.Values.Server.Host, client.nick, userData.LastLogin.Format("2006-01-02 15:04:05"))
							}
							log.Infof("WHOACC: Displayed account info for '%s'", client.accountName)
						}
					} else {
						send(conn, ":%s NOTICE %s :Account: %s (authenticated)\n",
							config.Values.Server.Host, client.nick, client.accountName)
					}
				} else {
					send(conn, ":%s NOTICE %s :You are not logged in to any account\n",
						config.Values.Server.Host, client.nick)
					log.Infof("WHOACC: User '%s' not authenticated", client.nick)
				}
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
			Host:     config.Values.Server.Host,
			Channels: []string{}, // Will be updated when joining channels
		}
		if err := s.RedisClient.RegisterClient(ctx, clientData); err != nil {
			log.Errorf("Failed to register client in Redis: %v", err)
			// Continue anyway - client is registered locally
		} else {
			if config.Values.Server.Debug {
		log.Debugf("Registered client %s in Redis", client.nick)
	}
		}
	}

	send(client.conn, ":%s %s %s :Welcome to the Internet Relay Network %s!%s@%s\n", config.Values.Server.Host, RplWelcome, client.nick, client.nick, client.user, config.Values.Server.Host)
	send(client.conn, ":%s %s %s :Your host is %s, running version %s\n", config.Values.Server.Host, RplYourHost, client.nick, config.Values.Server.Host, version.GetVersion())
	send(client.conn, ":%s %s %s :This server was created %s\n", config.Values.Server.Host, RplCreated, client.nick, time.Now().String())

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
		newRoom := createRoom(roomName)
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
			if config.Values.Server.Debug {
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
	for _, ident := range clientIDs {
		for _, cli := range s.Clients {
			if ident == cli.identifier {
				send(cli.conn, ":%s!%s@%s %s #%s\n", clientPtr.nick, clientPtr.user, config.Values.Server.Host, JoinCmd, roomPtr.name)
				names = append(names, cli.nick)
			}
		}
	}

	if roomPtr.topic == "" {
		send(clientPtr.conn, ":%s %s %s #%s :No topic is set\n", config.Values.Server.Host, RplTopic, clientPtr.nick, roomPtr.name)
	} else {
		send(clientPtr.conn, ":%s %s %s #%s :%s\n", config.Values.Server.Host, RplNoTopic, clientPtr.nick, roomPtr.name, roomPtr.topic)
	}

	// send list of all clients in room to user
	// "( "=" / "*" / "@" ) <channel> :[ "@" / "+" ] <nick> *( " " [ "@" / "+" ] <nick> )
	send(clientPtr.conn, ":%s %s %s = #%s :%s\n", config.Values.Server.Host, RplNameReply, clientPtr.nick, roomPtr.name, strings.Join(names[:], " "))

	send(clientPtr.conn, ":%s %s %s #%s :End of NAMES list\n", config.Values.Server.Host, RplEndOfNames, clientPtr.nick, roomPtr.name)
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
			if config.Values.Server.Debug {
		log.Debugf("Unregistered client %s from Redis", id.String())
	}
		}
	}

	s.ClientsMutex.Lock()
	defer s.ClientsMutex.Unlock()

	if len(s.Clients) < 2 {
		s.Clients = []Client{}
		return
	}

	var index int
	for num, iter := range s.Clients {
		if iter.identifier == id {
			index = num
			break
		}
	}
	if len(s.Clients) == 2 {
		if index == 0 {
			s.Clients = s.Clients[index+1:]
		} else {
			s.Clients = s.Clients[:index]
		}
	} else {
		s.Clients = append(s.Clients[:index], s.Clients[index+1:]...)
	}
	return
}

func (s *Server) IsUserInRoom(client Client, roomName string) (bool, *Room) {
	compareRoomName := roomName
	if strings.HasPrefix(roomName, "#") {
		compareRoomName = roomName[1:]
	}
	s.RoomsMutex.Lock()
	defer s.RoomsMutex.Unlock()

	for _, room := range s.Rooms {
		if strings.Compare(room.name, compareRoomName) == 0 {
			if _, ok := client.rooms[room.identifier]; ok {
				return true, &room
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

func createRoom(name string) Room {
	return Room{
		roomMux:    &sync.Mutex{},
		identifier: uuid.Must(uuid.NewRandom()),
		name:       name,
		clients:    make(map[uuid.UUID]bool),
		topic:      "",
		operators:  make(map[uuid.UUID]bool),
		voiced:     make(map[uuid.UUID]bool),
		founders:   make(map[uuid.UUID]bool),
		registered: false,
		topicLock:  true,  // Default: only ops can set topic
		noExternal: true,  // Default: no external messages
	}
}

func validNickCharset(test string) bool {
	re, err := regexp.Compile("^[-#&*_a-zA-Z0-9]{1,}$")
	if err != nil {
		fmt.Printf("Something went wrong validating the nickname\n")
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
	fmt.Printf(">> %s", message)
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
				if config.Values.Server.Debug {
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
				if config.Values.Server.Debug {
		log.Debugf("This pod is the leader, performing cleanup...")
	}
				// We are the leader, perform cleanup
				count, err := s.RedisClient.CleanupOrphanedClients(ctx)
				if err != nil {
					log.Errorf("Failed to cleanup orphaned clients: %v", err)
				} else if count > 0 {
					log.Infof("Cleaned up %d orphaned clients", count)
				} else {
					if config.Values.Server.Debug {
		log.Debugf("No orphaned clients found")
	}
				}

				// Release lock
				if err := s.RedisClient.ReleaseLeaderLock(ctx); err != nil {
					log.Errorf("Failed to release leader lock: %v", err)
				}
			} else {
				if config.Values.Server.Debug {
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
		if config.Values.Server.Debug {
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
			if config.Values.Server.Debug {
		log.Debugf("Unknown Redis event type: %s", event.Type)
	}
		}
	}

	log.Println("Redis event subscription ended")
}

// Redis event handlers (stubs for now, will implement in following commits)
func (s *Server) handleRedisPrivmsg(event *redis.Event) {
	// TODO: Implement PRIVMSG event handler
	if config.Values.Server.Debug {
		log.Debugf("Handling PRIVMSG event: %+v", event.Data)
	}
}

func (s *Server) handleRedisJoin(event *redis.Event) {
	// TODO: Implement JOIN event handler
	if config.Values.Server.Debug {
		log.Debugf("Handling JOIN event: %+v", event.Data)
	}
}

func (s *Server) handleRedisPart(event *redis.Event) {
	// TODO: Implement PART event handler
	if config.Values.Server.Debug {
		log.Debugf("Handling PART event: %+v", event.Data)
	}
}

func (s *Server) handleRedisQuit(event *redis.Event) {
	// TODO: Implement QUIT event handler
	if config.Values.Server.Debug {
		log.Debugf("Handling QUIT event: %+v", event.Data)
	}
}

func (s *Server) handleRedisNick(event *redis.Event) {
	// TODO: Implement NICK event handler
	if config.Values.Server.Debug {
		log.Debugf("Handling NICK event: %+v", event.Data)
	}
}

func (s *Server) handleRedisKick(event *redis.Event) {
	// TODO: Implement KICK event handler
	if config.Values.Server.Debug {
		log.Debugf("Handling KICK event: %+v", event.Data)
	}
}
