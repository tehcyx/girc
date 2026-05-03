package server

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/tehcyx/girc/internal/config"
	"github.com/tehcyx/girc/pkg/redis"
	"github.com/tehcyx/girc/pkg/version"
)

// handleCommand dispatches a single IRC command for client connected on conn.
// Returns false if the connection should be closed (e.g. QUIT), true otherwise.
func (s *Server) handleCommand(client *Client, conn net.Conn, cmd, args string) bool {
	if len(cmd) == 0 {
		return true
	} else if cmd == "CAP" {
		// Ignore CAP commands - we don't support capabilities
		// Modern clients send this, but we just silently ignore it
		log.Infof("CAP command ignored: %s", args)
		return true
	} else if cmd == QuitCmd {
		// send goodbye message
		return false
	} else if cmd == PassCmd && !client.registered {
		// Command: PASS Parameters: <password>
		// Store password for authentication during NICK/USER registration
		log.Infof("Processing PASS command")
		if len(args) == 0 {
			log.Infof("PASS: No password given")
			send(conn, ":%s %s PASS :Not enough parameters\n", s.getConfig().Server.Host, ErrNeedMoreParams)
			return true
		}
		client.password = args
		log.Infof("PASS: Password stored for authentication")
		return true
	} else if cmd == RegisterCmd {
		// Command: REGISTER Parameters: <username> <password> <email>
		// Example: REGISTER alice mySecurePass123 alice@example.com
		log.Infof("Processing REGISTER command")

		parts := strings.Fields(args)
		if len(parts) < 3 {
			log.Infof("REGISTER: Not enough parameters")
			send(conn, ":%s %s %s :Not enough parameters (usage: REGISTER <username> <password> <email>)\n",
				s.getConfig().Server.Host, ErrNeedMoreParams, RegisterCmd)
			return true
		}

		username := parts[0]
		password := parts[1]
		email := parts[2]

		log.Infof("REGISTER: Attempting to register user '%s' with email '%s'", username, email)

		// Validate username (use same rules as nickname)
		if !validNickCharset(username) {
			log.Infof("REGISTER: Invalid username charset: %s", username)
			send(conn, ":%s %s :Invalid username (must contain only letters, numbers, -, _, [, ], \\, `, ^, {, })\n",
				s.getConfig().Server.Host, ErrNickInvalid)
			return true
		}

		// Validate password strength
		if !IsValidPassword(password) {
			log.Infof("REGISTER: Weak password for user '%s'", username)
			send(conn, ":%s %s :Password does not meet security requirements (minimum 8 characters, maximum 72 characters)\n",
				s.getConfig().Server.Host, ErrWeakPassword)
			return true
		}

		// Validate email (basic validation)
		if !IsValidEmail(email) {
			log.Infof("REGISTER: Invalid email for user '%s': %s", username, email)
			send(conn, ":%s %s :Invalid email address\n",
				s.getConfig().Server.Host, ErrInvalidEmail)
			return true
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
					s.getConfig().Server.Host, ErrRegisteredAlready)
				return true
			}
			// Error could mean user not found (expected) or Redis error
			// If it's just "user not found", that's fine - continue with registration
		} else {
			log.Warn("REGISTER: Redis not available, cannot persist user registration")
			send(conn, ":%s %s :Registration service not available\n",
				s.getConfig().Server.Host, ErrNoSuchService)
			return true
		}

		// Hash the password
		hashedPassword, err := HashPassword(password)
		if err != nil {
			log.Errorf("REGISTER: Failed to hash password for user '%s': %v", username, err)
			send(conn, ":%s %s :Registration failed - please try again later\n",
				s.getConfig().Server.Host, ErrNoSuchService)
			return true
		}

		// Store user in Redis
		ctx := context.Background()
		err = s.RedisClient.StoreUser(ctx, username, hashedPassword, email)
		if err != nil {
			log.Errorf("REGISTER: Failed to store user '%s': %v", username, err)
			send(conn, ":%s %s :Registration failed - please try again later\n",
				s.getConfig().Server.Host, ErrNoSuchService)
			return true
		}

		log.Infof("REGISTER: Successfully registered user '%s'", username)
		send(conn, ":%s %s %s %s :Account created successfully. To login: send PASS <password>, then NICK %s, then USER %s 0 * :Your Name\n",
			s.getConfig().Server.Host, RplRegistered, username, username, username, username)
		return true
	} else if cmd == NickCmd {
		// Command: NICK Parameters: <nickname> [ <hopcount> ]
		// Allowed both before and after registration.
		log.Infof("Processing NICK command with args: '%s'", args)
		newNick := strings.TrimSpace(args)
		if len(newNick) == 0 {
			log.Infof("NICK: No nickname given")
			send(conn, ":%s %s :No nickname given\n", s.getConfig().Server.Host, ErrNickNull)
			return true
		}
		if !validNickCharset(newNick) {
			log.Infof("NICK: Invalid nickname charset: %s", newNick)
			send(conn, ":%s %s * %s :Erroneous nickname\n", s.getConfig().Server.Host, ErrNickInvalid, newNick)
			return true
		}
		// Check nick availability - skip self if already registered.
		localExists := false
		if newNick != client.nick {
			s.ClientsMutex.Lock()
			for _, c := range s.Clients {
				if strings.EqualFold(c.nick, newNick) && c.identifier != client.identifier {
					localExists = true
					break
				}
			}
			s.ClientsMutex.Unlock()
		}

		redisExists := false
		if s.RedisClient != nil && newNick != client.nick {
			ctx := context.Background()
			available, err := s.RedisClient.IsNickAvailable(ctx, newNick)
			if err != nil {
				log.Errorf("Failed to check nick availability in Redis: %v", err)
			} else {
				redisExists = !available
			}
		}

		if localExists || redisExists {
			log.Infof("NICK: Nickname '%s' already in use", newNick)
			send(conn, ":%s %s * %s :Nickname is already in use\n", s.getConfig().Server.Host, ErrNickInUse, newNick)
			return true
		}

		if client.registered {
			// Nick change after registration — broadcast to user + all shared channels.
			oldNick := client.nick
			client.nick = newNick
			s.UpdateClientNick(client.identifier, newNick)

			if s.RedisClient != nil {
				ctx := context.Background()
				_ = s.RedisClient.UpdateClientNick(ctx, client.identifier.String(), oldNick, newNick)
				event := redis.Event{
					Type: "NICK",
					Data: map[string]interface{}{
						"uuid":     client.identifier.String(),
						"old_nick": oldNick,
						"new_nick": newNick,
						"user":     client.user,
						"pod_id":   s.RedisClient.PodID(),
					},
				}
				if err := s.RedisClient.PublishEvent(ctx, event); err != nil {
					log.Errorf("Failed to publish NICK event: %v", err)
				}
			}

			// Notify: self + all clients in shared channels (deduped).
			nickMsg := fmt.Sprintf(":%s!%s@%s NICK :%s\r\n", oldNick, client.user, s.getConfig().Server.Host, newNick)
			notified := map[uuid.UUID]bool{client.identifier: true}
			conn.Write([]byte(nickMsg))

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
				if room == nil {
					continue
				}
				room.roomMux.Lock()
				memberIDs := make([]uuid.UUID, 0, len(room.clients))
				for id := range room.clients {
					memberIDs = append(memberIDs, id)
				}
				room.roomMux.Unlock()
				for _, id := range memberIDs {
					if notified[id] {
						continue
					}
					s.ClientsMutex.Lock()
					for i := range s.Clients {
						if s.Clients[i].identifier == id {
							s.Clients[i].conn.Write([]byte(nickMsg))
							notified[id] = true
							break
						}
					}
					s.ClientsMutex.Unlock()
				}
			}
		} else {
			log.Infof("NICK: Setting nickname to '%s'", newNick)
			client.nick = newNick
			if client.user == "" {
				client.user = "*"
			}
		}
		return true
	} else if cmd == UserCmd && !client.registered {
		// Command: USER Parameters: <user> <mode> <unused> <realname>
		// Example: USER shout-user 0 * :Shout User
		log.Infof("Processing USER command with args: '%s'", args)
		if len(args) == 0 {
			// RFC 2812: 461 ERR_NEEDMOREPARAMS (not 462)
			log.Infof("USER: No parameters given")
			send(conn, ":%s %s %s USER :Not enough parameters\n", s.getConfig().Server.Host, ErrNeedMoreParams, client.nick)
			return true
		}
		split := strings.SplitN(args, ":", 2)
		log.Infof("USER: split into %d parts", len(split))
		if len(split) < 2 || len(strings.Fields(split[0])) < 3 {
			log.Infof("USER: Not enough parameters")
			send(conn, ":%s %s %s USER :Not enough parameters\n", s.getConfig().Server.Host, ErrNeedMoreParams, client.nick)
			return true
		}
		args1fields := strings.Fields(split[0])
		args2 := split[1]

		client.user = args1fields[0]
		client.mode, _ = strconv.Atoi(args1fields[1])
		client.unused = args1fields[2]
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
						s.getConfig().Server.Host, RplLoggedIn, client.nick, client.accountName, client.accountName)
				} else {
					// Authentication failed
					log.Warnf("USER: Password verification failed for registered account '%s'", client.nick)
					send(conn, ":%s %s :Password incorrect\n", s.getConfig().Server.Host, ErrPasswdMismatch)
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

		// Registration requires both NICK and USER.
		if client.nick == "" {
			// User sent USER before NICK; need to wait for NICK.
			// Store user params; registration completes when NICK arrives.
			// (clients that send NICK first are handled above)
			log.Infof("USER: nick not set yet, storing user params and waiting for NICK")
			return true
		}
		log.Infof("USER: Calling AddClient for nick='%s', user='%s', realname='%s', authenticated=%v",
			client.nick, client.user, client.realname, client.authenticated)
		s.AddClient(*client)
		client.registered = true
		log.Infof("USER: Registration completed")

		// Start per-client PING goroutine if configured.
		if s.PingInterval > 0 {
			go s.pingClient(conn, client.identifier)
		}
	} else if cmd == PingCmd {
		if len(args) == 0 {
			send(conn, ":%s %s :No origin specified\n", s.getConfig().Server.Host, ErrNoOrigin)
			return true
		}
		send(conn, ":%s %s %s\n", s.getConfig().Server.Host, PingPongCmd, args)
	} else if cmd == PingPongCmd {
		// Client PONG in response to our PING: reset the read deadline.
		conn.SetReadDeadline(time.Now().Add(s.ClientTimeout))
	} else if cmd == JoinCmd && client.registered {
		if len(args) == 0 {
			send(conn, ":%s %s %s :Not enough parameters\n", s.getConfig().Server.Host, ErrNeedMoreParams, JoinCmd)
			return true
		}
		rooms := strings.Split(args, ",")
		for _, roomName := range rooms {
			s.JoinRoomByName(*client, roomName)
		}
	} else if cmd == PrivmsgCmd && client.registered {
		if len(args) == 0 {
			send(conn, ":%s %s :No recipient given\n", s.getConfig().Server.Host, ErrNoRecipient)
			return true
		}
		// RFC 1459: PRIVMSG <target> :<text>
		// The trailing parameter starts at the first " :" — use SplitN to
		// preserve colons in the message body.
		parts := strings.SplitN(args, " :", 2)
		if len(parts) < 2 {
			send(conn, ":%s %s :No text to send\n", s.getConfig().Server.Host, ErrNoTextToSend)
			return true
		}
		target := strings.TrimSpace(parts[0])
		chatMessage := parts[1]
		if strings.HasPrefix(target, "#") || strings.HasPrefix(target, "$") {
			// is channel or mask
			if validMessageMask(target) {

				log.Infof("Masking is not implemented, swallowing message: %s", args)
				// ERR_WILDTOPLEVEL
				// ERR_NOTOPLEVEL
				// ERR_TOOMANYTARGETS
			} else {
				if inChannel, room := s.IsUserInRoom(*client, target); inChannel {
					room.roomMux.Lock()
					for ident := range room.clients {
						if ident == client.identifier {
							continue
						}
						for _, cli := range s.Clients {
							if ident == cli.identifier {
								send(cli.conn, ":%s!%s@%s %s %s :%s\n", client.nick, client.user, s.getConfig().Server.Host, PrivmsgCmd, target, chatMessage)
							}
						}
					}
					room.roomMux.Unlock()

					// Publish to Redis for cross-pod delivery.
					if s.RedisClient != nil {
						ctx := context.Background()
						event := redis.Event{
							Type: "PRIVMSG",
							Data: map[string]interface{}{
								"from_nick": client.nick,
								"from_user": client.user,
								"target":    target,
								"message":   chatMessage,
								"pod_id":    s.RedisClient.PodID(),
							},
						}
						if err := s.RedisClient.PublishEvent(ctx, event); err != nil {
							log.Errorf("[PRIVMSG] Failed to publish to Redis: %v", err)
						}
					}
				} else {
					send(conn, ":%s %s %s %s :Cannot send to channel\n", s.getConfig().Server.Host, ErrCannotSendToChan, client.nick, target)
				}
			}
		} else if validNickCharset(target) {
			// not a channel or mask, let's find out if the requested user is connected
			targetClient := s.ClientByName(target)
			if targetClient.nick == "" {
				send(conn, ":%s %s %s %s :No such nick/channel\n", s.getConfig().Server.Host, ErrNoSuchNick, client.nick, target)
			} else {
				send(targetClient.conn, ":%s!%s@%s %s %s :%s\n", client.nick, client.user, s.getConfig().Server.Host, PrivmsgCmd, target, chatMessage)

				// Publish to Redis for cross-pod direct message delivery.
				if s.RedisClient != nil {
					ctx := context.Background()
					event := redis.Event{
						Type: "PRIVMSG",
						Data: map[string]interface{}{
							"from_nick": client.nick,
							"from_user": client.user,
							"target":    target,
							"message":   chatMessage,
							"pod_id":    s.RedisClient.PodID(),
						},
					}
					if err := s.RedisClient.PublishEvent(ctx, event); err != nil {
						log.Errorf("[PRIVMSG] Failed to publish to Redis: %v", err)
					}
				}
			}
		}
	} else if cmd == PartCmd && client.registered {
		if len(args) == 0 {
			send(conn, ":%s %s :No recipient given\n", s.getConfig().Server.Host, ErrNoRecipient)
			return true
		}
		// RFC: PART <channel>{,<channel>} [:<reason>]
		partParts := strings.SplitN(args, " :", 2)
		partTargets := strings.Split(strings.TrimSpace(partParts[0]), ",")
		partMessage := ""
		if len(partParts) >= 2 {
			partMessage = partParts[1]
		}

		for _, target := range partTargets {
			target = strings.TrimSpace(target)
			if !s.RoomExists(target) {
				send(conn, ":%s %s %s :No such channel\n", s.getConfig().Server.Host, ErrNoSuchChannel, target)
				continue
			}
			inChannel, room := s.IsUserInRoom(*client, target)
			if !inChannel {
				send(conn, ":%s %s %s :You're not on that channel\n", s.getConfig().Server.Host, ErrNotOnChannel, target)
				continue
			}

			// Collect channel members while holding the lock briefly,
			// then release before sending. This avoids defer-in-loop deadlock.
			room.roomMux.Lock()
			memberIDs := make([]uuid.UUID, 0, len(room.clients))
			for id := range room.clients {
				memberIDs = append(memberIDs, id)
			}
			delete(room.clients, client.identifier)
			room.roomMux.Unlock()

			// Remove room from client's room list.
			delete(client.rooms, room.identifier)

			// Update server's copy of client.
			s.ClientsMutex.Lock()
			for i := range s.Clients {
				if s.Clients[i].identifier == client.identifier {
					delete(s.Clients[i].rooms, room.identifier)
					break
				}
			}
			s.ClientsMutex.Unlock()

			// Send PART only to channel members (not all clients).
			partMsg := fmt.Sprintf(":%s!%s@%s %s %s :%s\r\n",
				client.nick, client.user, s.getConfig().Server.Host, PartCmd, target, partMessage)
			s.ClientsMutex.Lock()
			for _, memberID := range memberIDs {
				for i := range s.Clients {
					if s.Clients[i].identifier == memberID {
						s.Clients[i].conn.Write([]byte(partMsg))
						break
					}
				}
			}
			// Also send PART to the parting client themselves (they were in memberIDs before removal).
			conn.Write([]byte(partMsg))
			s.ClientsMutex.Unlock()

			// Update Redis if enabled.
			if s.RedisClient != nil {
				ctx := context.Background()
				channelName := target
				if !strings.HasPrefix(channelName, "#") {
					channelName = "#" + channelName
				}
				if err := s.RedisClient.PartChannel(ctx, client.identifier.String(), channelName); err != nil {
					log.Errorf("Failed to part channel in Redis: %v", err)
				}
				event := redis.Event{
					Type: "PART",
					Data: map[string]interface{}{
						"uuid":    client.identifier.String(),
						"nick":    client.nick,
						"user":    client.user,
						"channel": channelName,
						"message": partMessage,
						"pod_id":  s.RedisClient.PodID(),
					},
				}
				if err := s.RedisClient.PublishEvent(ctx, event); err != nil {
					log.Errorf("Failed to publish PART event: %v", err)
				}
			}
		}
	} else if cmd == WhoisCmd && client.registered {
		// Command: WHOIS <nickname>
		// Returns information about the specified user
		if len(args) == 0 {
			send(conn, ":%s %s :No nickname given\n", s.getConfig().Server.Host, ErrNoRecipient)
			return true
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
			send(conn, ":%s %s %s :No such nick/channel\n", s.getConfig().Server.Host, ErrNoSuchNick, targetNick)
		} else {
			// RPL_WHOISUSER: 311 <nick> <user> <host> * :<real name>
			send(conn, ":%s %s %s %s %s %s * :%s\n",
				s.getConfig().Server.Host, RplWhoisUser, client.nick,
				targetClient.nick, targetClient.user, s.getConfig().Server.Host, targetClient.realname)

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
						s.getConfig().Server.Host, RplWhoIsChannels, client.nick,
						targetClient.nick, strings.Join(channels, " "))
				}
			}

			// RPL_WHOISSERVER: 312 <nick> <server> :<server info>
			send(conn, ":%s %s %s %s %s :%s\n",
				s.getConfig().Server.Host, RplWhoisServer, client.nick,
				targetClient.nick, s.getConfig().Server.Name, s.getConfig().Server.Host)

			// RPL_WHOISOPERATOR: 313 <nick> :is an IRC operator (if applicable)
			if targetClient.operator {
				send(conn, ":%s %s %s %s :is an IRC operator\n",
					s.getConfig().Server.Host, RplWhoisOperator, client.nick, targetClient.nick)
			}

			// RPL_ENDOFWHOIS: 318 <nick> :End of WHOIS list
			send(conn, ":%s %s %s %s :End of WHOIS list\n",
				s.getConfig().Server.Host, RplEndOfWhois, client.nick, targetClient.nick)
		}
	} else if cmd == KickCmd && client.registered {
		// Command: KICK <channel> <user> [<comment>]
		// Kicks a user out of a channel (requires operator privileges)
		if len(args) == 0 {
			send(conn, ":%s %s %s :Not enough parameters\n", s.getConfig().Server.Host, ErrNeedMoreParams, KickCmd)
			return true
		}
		parts := strings.SplitN(args, " ", 2)
		if len(parts) < 2 {
			send(conn, ":%s %s %s :Not enough parameters\n", s.getConfig().Server.Host, ErrNeedMoreParams, KickCmd)
			return true
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
			send(conn, ":%s %s %s :No such channel\n", s.getConfig().Server.Host, ErrNoSuchChannel, channelName)
			return true
		}

		// Check if kicker is in the channel
		inChannel, room := s.IsUserInRoom(*client, channelName)
		if !inChannel {
			send(conn, ":%s %s %s :You're not on that channel\n", s.getConfig().Server.Host, ErrNotOnChannel, channelName)
			return true
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
			send(conn, ":%s %s %s :You're not channel operator\n", s.getConfig().Server.Host, ErrChanOpPrivsNeeded, channelName)
			return true
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
			send(conn, ":%s %s %s :No such nick/channel\n", s.getConfig().Server.Host, ErrNoSuchNick, targetNick)
			return true
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
				s.getConfig().Server.Host, ErrUserNotInChannel, targetNick, channelName)
			return true
		}

		// Perform the kick - notify all channel members
		room.roomMux.Lock()
		for memberID := range room.clients {
			for i := range s.Clients {
				if s.Clients[i].identifier == memberID {
					send(s.Clients[i].conn, ":%s!%s@%s KICK %s %s :%s\n",
						client.nick, client.user, s.getConfig().Server.Host,
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
	} else if cmd == InviteCmd && client.registered {
		// Command: INVITE <nickname> <channel>
		// Invites a user to a channel
		if len(args) == 0 {
			send(conn, ":%s %s %s :Not enough parameters\n", s.getConfig().Server.Host, ErrNeedMoreParams, InviteCmd)
			return true
		}
		parts := strings.SplitN(args, " ", 2)
		if len(parts) < 2 {
			send(conn, ":%s %s %s :Not enough parameters\n", s.getConfig().Server.Host, ErrNeedMoreParams, InviteCmd)
			return true
		}

		targetNick := strings.TrimSpace(parts[0])
		channelName := strings.TrimSpace(parts[1])

		// Check if channel exists
		if !s.RoomExists(channelName) {
			send(conn, ":%s %s %s :No such channel\n", s.getConfig().Server.Host, ErrNoSuchChannel, channelName)
			return true
		}

		// Check if inviter is in the channel
		inChannel, room := s.IsUserInRoom(*client, channelName)
		if !inChannel {
			send(conn, ":%s %s %s :You're not on that channel\n", s.getConfig().Server.Host, ErrNotOnChannel, channelName)
			return true
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
			send(conn, ":%s %s %s :No such nick/channel\n", s.getConfig().Server.Host, ErrNoSuchNick, targetNick)
			return true
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
				s.getConfig().Server.Host, ErrUserOnChannel, targetNick, channelName)
			return true
		}

		// Send INVITE notification to target user
		send(targetClient.conn, ":%s!%s@%s INVITE %s %s\n",
			client.nick, client.user, s.getConfig().Server.Host,
			targetNick, channelName)

		// Send RPL_INVITING confirmation to inviter
		send(conn, ":%s %s %s %s %s\n",
			s.getConfig().Server.Host, RplInviting, client.nick,
			channelName, targetNick)

		log.Infof("[INVITE] %s invited %s to %s", client.nick, targetNick, channelName)
	} else if cmd == ListCmd && client.registered {
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
					s.getConfig().Server.Host, RplList, client.nick,
					channelName, visibleCount, topic)
			}
		}

		// Send RPL_LISTEND
		send(conn, ":%s %s %s :End of LIST\n",
			s.getConfig().Server.Host, RplListEnd, client.nick)

		log.Infof("[LIST] Sent %d channels to %s", len(channelsToList), client.nick)
	} else if cmd == TopicCmd && client.registered {
		// Command: TOPIC <channel> [<topic>]
		// Query or set channel topic
		if len(args) == 0 {
			send(conn, ":%s %s %s :Not enough parameters\n", s.getConfig().Server.Host, ErrNeedMoreParams, TopicCmd)
			return true
		}
		parts := strings.SplitN(args, ":", 2)
		channelName := strings.TrimSpace(parts[0])

		// Check if channel exists
		if !s.RoomExists(channelName) {
			send(conn, ":%s %s %s :No such channel\n", s.getConfig().Server.Host, ErrNoSuchChannel, channelName)
			return true
		}

		// Check if user is in the channel
		inChannel, _ := s.IsUserInRoom(*client, channelName)
		if !inChannel {
			send(conn, ":%s %s %s :You're not on that channel\n", s.getConfig().Server.Host, ErrNotOnChannel, channelName)
			return true
		}

		// Get the actual room pointer from server's slice
		room := s.GetRoom(channelName)
		if room == nil {
			send(conn, ":%s %s %s :No such channel\n", s.getConfig().Server.Host, ErrNoSuchChannel, channelName)
			return true
		}

		// Query topic (no second parameter)
		if len(parts) == 1 {
			room.roomMux.Lock()
			topic := room.topic
			room.roomMux.Unlock()

			if topic == "" {
				// RPL_NOTOPIC: 331 <client> <channel> :No topic is set
				send(conn, ":%s %s %s %s :No topic is set\n",
					s.getConfig().Server.Host, RplNoTopic, client.nick, channelName)
			} else {
				// RPL_TOPIC: 332 <client> <channel> :<topic>
				send(conn, ":%s %s %s %s :%s\n",
					s.getConfig().Server.Host, RplTopic, client.nick, channelName, topic)
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
					s.getConfig().Server.Host, ErrChanOpPrivsNeeded, channelName)
				log.Infof("[TOPIC] %s denied setting topic for %s (not operator/founder)", client.nick, channelName)
				return true
			}

			// Set the topic
			room.roomMux.Lock()
			room.topic = newTopic
			room.roomMux.Unlock()

			// Persist topic to Redis.
			if s.RedisClient != nil {
				if err := s.RedisClient.SetChannelTopic(context.Background(), "#"+strings.TrimPrefix(channelName, "#"), newTopic); err != nil {
					log.Errorf("[TOPIC] Failed to persist topic to Redis: %v", err)
				}
			}

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
							client.nick, client.user, s.getConfig().Server.Host,
							channelName, newTopic)
						break
					}
				}
				s.ClientsMutex.Unlock()
			}

			log.Infof("[TOPIC] %s set topic for %s: %s", client.nick, channelName, newTopic)
		}
	} else if cmd == OperCmd && client.registered {
		// Command: OPER <username> <password>
		// Authenticate as IRC operator
		if len(args) == 0 {
			send(conn, ":%s %s %s :Not enough parameters\n", s.getConfig().Server.Host, ErrNeedMoreParams, OperCmd)
			return true
		}

		// Parse username and password
		parts := strings.Fields(args)
		if len(parts) < 2 {
			send(conn, ":%s %s %s :Not enough parameters\n", s.getConfig().Server.Host, ErrNeedMoreParams, OperCmd)
			log.Infof("[OPER] %s failed: insufficient parameters", client.nick)
			return true
		}

		username := parts[0]
		password := parts[1]

		log.Infof("[OPER] %s attempting to authenticate as '%s'", client.nick, username)

		// Load users from config
		users, err := config.LoadUsers()
		if err != nil {
			send(conn, ":%s %s :Configuration error\n", s.getConfig().Server.Host, ErrPasswdMismatch)
			log.Errorf("[OPER] Failed to load users: %v", err)
			return true
		}

		// Authenticate user
		user, authenticated := config.AuthenticateUser(username, password, users)
		if !authenticated {
			// ERR_PASSWDMISMATCH: 464 :Password incorrect
			send(conn, ":%s %s :Password incorrect\n", s.getConfig().Server.Host, ErrPasswdMismatch)
			log.Infof("[OPER] %s failed: invalid credentials for '%s'", client.nick, username)
			return true
		}

		// Check if user has operator privileges
		if !user.IsOper {
			// ERR_NOOPERHOST: 491 :No O-lines for your host
			send(conn, ":%s %s :No O-lines for your host\n", s.getConfig().Server.Host, ErrNoOperHost)
			log.Infof("[OPER] %s failed: '%s' is not authorized as operator", client.nick, username)
			return true
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
			s.getConfig().Server.Host, RplYoureOper, client.nick)
		log.Infof("[OPER] %s successfully authenticated as operator '%s'", client.nick, username)
	} else if cmd == NoticeCmd && client.registered {
		// Command: NOTICE <target> :<message>
		// Similar to PRIVMSG but must not trigger automatic replies
		if len(args) == 0 {
			// NOTICE should fail silently - no error responses per RFC
			log.Infof("[NOTICE] No recipient given, failing silently")
			return true
		} else {
			parts := strings.SplitN(args, " :", 2)
			if len(parts) < 2 {
				// NOTICE should fail silently
				log.Infof("[NOTICE] No text to send, failing silently")
				return true
			}

			target := strings.TrimSpace(parts[0])
			noticeMessage := parts[1]

			if len(target) == 0 || len(noticeMessage) == 0 {
				// NOTICE should fail silently
				log.Infof("[NOTICE] Empty target or message, failing silently")
				return true
			}

			if strings.HasPrefix(target, "#") || strings.HasPrefix(target, "$") {
				// Channel or mask target
				if validMessageMask(target) {
					// Masking is not implemented
					log.Infof("[NOTICE] Masking not implemented, failing silently")
					return true
				} else {
					// Send to channel
					if inChannel, room := s.IsUserInRoom(*client, target); inChannel {
						room.roomMux.Lock()
						for ident := range room.clients {
							if ident == client.identifier {
								continue // Don't send to sender
							}
							for _, cli := range s.Clients {
								if ident == cli.identifier {
									send(cli.conn, ":%s!%s@%s %s %s :%s\n",
										client.nick, client.user, s.getConfig().Server.Host,
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
						client.nick, client.user, s.getConfig().Server.Host,
						NoticeCmd, target, noticeMessage)
					log.Infof("[NOTICE] %s sent notice to %s", client.nick, target)
				}
			}
		}
	} else if cmd == KillCmd && client.registered {
		// Command: KILL <nickname> :<reason>
		// Operator command to forcefully disconnect a user
		log.Infof("[KILL] %s attempting KILL: '%s'", client.nick, args)

		// Check if user is operator
		if !client.operator {
			send(conn, ":%s %s :Permission denied- You're not an IRC operator\n",
				s.getConfig().Server.Host, ErrNoPrivileges)
			log.Infof("[KILL] %s denied - not an operator", client.nick)
			return true
		}

		// Parse arguments
		parts := strings.SplitN(args, ":", 2)
		if len(parts) < 2 {
			send(conn, ":%s %s %s :Not enough parameters\n",
				s.getConfig().Server.Host, ErrNeedMoreParams, KillCmd)
			return true
		}

		targetNick := strings.TrimSpace(parts[0])
		reason := strings.TrimSpace(parts[1])

		if len(targetNick) == 0 {
			send(conn, ":%s %s * :No such nick/channel\n",
				s.getConfig().Server.Host, ErrNoSuchNick)
			return true
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
				s.getConfig().Server.Host, ErrNoSuchNick, targetNick)
			log.Infof("[KILL] Target %s not found", targetNick)
			return true
		}

		// Build KILL message and QUIT notification
		killMsg := fmt.Sprintf("Killed by %s (%s)", client.nick, reason)

		// Send QUIT notification to all users in the same channels
		// This lets other users know this client has been killed
		if targetClient.nick != "" {
			quitMsg := fmt.Sprintf(":%s!%s@%s QUIT :Killed: %s\r\n",
				targetClient.nick, targetClient.user, s.getConfig().Server.Host, killMsg)

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

	} else if cmd == WallopsCmd && client.registered {
		// Command: WALLOPS :<message>
		// Send message to all users with +w (wallops) mode set
		log.Infof("[WALLOPS] %s sending wallops: '%s'", client.nick, args)

		// Check if user is operator
		if !client.operator {
			send(conn, ":%s %s :Permission denied- You're not an IRC operator\n",
				s.getConfig().Server.Host, ErrNoPrivileges)
			log.Infof("[WALLOPS] %s denied - not an operator", client.nick)
			return true
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
				s.getConfig().Server.Host, ErrNeedMoreParams, WallopsCmd)
			return true
		}

		// Send to all users with +w mode
		wallopsMsg := fmt.Sprintf(":%s!%s@%s WALLOPS :%s\r\n",
			client.nick, client.user, s.getConfig().Server.Host, message)

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

	} else if cmd == ChanRegCmd && client.registered {
		// Command: CHANREG <#channel> [:<description>]
		// Register a channel and become its founder
		log.Infof("[CHANREG] %s attempting to register channel: '%s'", client.nick, args)

		// Must be authenticated
		if !client.authenticated || client.accountName == "" {
			send(conn, ":%s %s :You must be logged in to register a channel\n",
				s.getConfig().Server.Host, ErrAccountRequired)
			log.Infof("[CHANREG] %s denied - not authenticated", client.nick)
			return true
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
				s.getConfig().Server.Host, ErrNeedMoreParams, ChanRegCmd)
			return true
		}

		// Validate channel name
		if !strings.HasPrefix(channelName, "#") || len(channelName) < 2 {
			send(conn, ":%s %s %s :No such channel\n",
				s.getConfig().Server.Host, ErrNoSuchChannel, channelName)
			return true
		}

		// Check if user is in the channel
		inChannel, room := s.IsUserInRoom(*client, channelName)
		if !inChannel {
			send(conn, ":%s %s %s :You're not on that channel\n",
				s.getConfig().Server.Host, ErrNotOnChannel, channelName)
			log.Infof("[CHANREG] %s denied - not in channel %s", client.nick, channelName)
			return true
		}

		// Check if user is channel operator
		room.roomMux.Lock()
		isOp := room.operators[client.identifier]
		isFounder := room.founders[client.identifier]
		room.roomMux.Unlock()

		if !isOp && !isFounder {
			send(conn, ":%s %s %s :You're not channel operator\n",
				s.getConfig().Server.Host, ErrChanOpPrivsNeeded, channelName)
			log.Infof("[CHANREG] %s denied - not operator in %s", client.nick, channelName)
			return true
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
					s.getConfig().Server.Host, ErrChanRegisteredAlready, channelName)
				log.Infof("[CHANREG] %s denied - %s already registered", client.nick, channelName)
				return true
			}

			// Register the channel
			err = s.RedisClient.RegisterChannel(ctx, channelName, client.accountName, description)
			if err != nil {
				send(conn, ":%s %s :Failed to register channel\n",
					s.getConfig().Server.Host, ErrNoSuchService)
				log.Errorf("[CHANREG] Failed to register %s: %v", channelName, err)
				return true
			}

			// Mark channel as registered and add user as founder
			room.roomMux.Lock()
			room.registered = true
			room.founders[client.identifier] = true
			room.roomMux.Unlock()

			// Success!
			send(conn, ":%s %s %s :Channel registered successfully\n",
				s.getConfig().Server.Host, RplChanRegistered, channelName)
			log.Infof("[CHANREG] %s successfully registered %s as founder %s",
				client.nick, channelName, client.accountName)
		} else {
			// No Redis - cannot register channels
			send(conn, ":%s %s :Channel registration requires Redis\n",
				s.getConfig().Server.Host, ErrNoSuchService)
			log.Infof("[CHANREG] %s denied - Redis not available", client.nick)
		}

	} else if cmd == "NAMES" && client.registered {
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
				inChannel, _ := s.IsUserInRoom(*client, channelName)
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
						s.getConfig().Server.Host, RplNameReply, client.nick,
						channelName, strings.Join(names, " "))
				}
			}
		}

		// Send RPL_ENDOFNAMES: 366 <client> <channel> :End of NAMES list
		if len(channelsToList) > 0 {
			for _, channelName := range channelsToList {
				send(conn, ":%s %s %s #%s :End of NAMES list\n",
					s.getConfig().Server.Host, RplEndOfNames, client.nick, channelName)
			}
		} else {
			// No channels specified or user not in any channels
			send(conn, ":%s %s %s * :End of NAMES list\n",
				s.getConfig().Server.Host, RplEndOfNames, client.nick)
		}

		log.Infof("[NAMES] Sent names for %d channels to %s", len(channelsToList), client.nick)
	} else if cmd == WhoCmd && client.registered {
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
				inChannel, room := s.IsUserInRoom(*client, channelName)
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
				s.getConfig().Server.Host, RplWhoReply, client.nick,
				channelName, user.user, s.getConfig().Server.Host,
				s.getConfig().Server.Host, user.nick, flags, user.realname)
		}

		// Send RPL_ENDOFWHO: 315 <client> <name> :End of WHO list
		endMask := mask
		if endMask == "" {
			endMask = "*"
		}
		send(conn, ":%s %s %s %s :End of WHO list\n",
			s.getConfig().Server.Host, RplEndOfWho, client.nick, endMask)

		log.Infof("[WHO] Sent WHO reply for %d users to %s", len(usersToList), client.nick)
	} else if cmd == AwayCmd && client.registered {
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
						s.getConfig().Server.Host, RplUnAway, client.nick)

					log.Infof("[AWAY] %s is no longer away", client.nick)
				} else {
					// Set away
					s.Clients[i].away = true
					s.Clients[i].awayMessage = message

					// RPL_NOWAWAY: 306 <client> :You have been marked as being away
					send(conn, ":%s %s %s :You have been marked as being away\n",
						s.getConfig().Server.Host, RplNowAway, client.nick)

					log.Infof("[AWAY] %s is now away: %s", client.nick, message)
				}
				break
			}
		}
		s.ClientsMutex.Unlock()
	} else if cmd == VersionCmd && client.registered {
		// Command: VERSION [<target>]
		// Returns the version of the server program
		// For now we only support VERSION without target (query this server)

		// RFC 1459: RPL_VERSION "351"
		// Format: 351 <client> <version>.<debuglevel> <server> :<comments>
		versionStr := version.GetVersion()
		debugLevel := "0"
		serverName := s.getConfig().Server.Host
		comments := "IRC Server written in Go"

		send(conn, ":%s %s %s %s.%s %s :%s\n",
			s.getConfig().Server.Host, RplVersion, client.nick,
			versionStr, debugLevel, serverName, comments)

		log.Infof("[VERSION] %s requested server version: %s", client.nick, versionStr)
	} else if cmd == MotdCmd && client.registered {
		// Command: MOTD [<target>]
		// Returns the Message of the Day
		// For now we only support MOTD without target (query this server)

		// RFC 1459: RPL_MOTDSTART "375", RPL_MOTD "372", RPL_ENDOFMOTD "376"
		serverName := s.getConfig().Server.Host

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
	} else if cmd == IsonCmd && client.registered {
		// Command: ISON <nickname> [<nickname> ...]
		// Check which of the specified nicknames are currently online
		// Returns a space-separated list of online nicknames

		// RFC 1459: RPL_ISON "303"
		nicknames := strings.Fields(args)
		var onlineNicks []string

		if len(nicknames) == 0 {
			// If no nicknames provided, return empty ISON reply
			send(conn, ":%s %s %s :\n",
				s.getConfig().Server.Host, RplIson, client.nick)
			return true
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
			s.getConfig().Server.Host, RplIson, client.nick, onlineList)

		log.Infof("[ISON] %s checked %d nicknames, %d online",
			client.nick, len(nicknames), len(onlineNicks))
	} else if cmd == UserhostCmd && client.registered {
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
				s.getConfig().Server.Host, RplUserhost, client.nick)
			return true
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
			s.getConfig().Server.Host, RplUserhost, client.nick, replyList)

		log.Infof("[USERHOST] %s queried %d nicknames, %d found",
			client.nick, len(nicknames), len(replies))
	} else if cmd == ModeCmd && client.registered {
		// Command: MODE <target> [<modestring> [<mode arguments>...]]
		// Two types: user modes and channel modes
		// User mode: MODE nickname +/-flags
		// Channel mode: MODE #channel +/-flags [parameters]

		parts := strings.Fields(args)
		if len(parts) == 0 {
			send(conn, ":%s %s %s :Not enough parameters\n",
				s.getConfig().Server.Host, ErrNeedMoreParams, ModeCmd)
			return true
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
						s.getConfig().Server.Host, ErrNoSuchChannel, client.nick, target)
					return true
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
					s.getConfig().Server.Host, RplChannelModeIs, client.nick, room.name, modeStr.String())
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
						s.getConfig().Server.Host, ErrNoSuchChannel, client.nick, target)
					return true
				}

				// Check if user is channel operator or founder
				if !room.operators[client.identifier] && !room.founders[client.identifier] {
					s.RoomsMutex.Unlock()
					send(conn, ":%s %s %s %s :You're not channel operator\n",
						s.getConfig().Server.Host, ErrChanOpPrivsNeeded, client.nick, target)
					return true
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
						s.getConfig().Server.Host, ErrUsersDontMatch, client.nick)
					return true
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
					s.getConfig().Server.Host, RplUModeIs, client.nick, modeStr.String())
				log.Infof("[MODE] %s queried own user modes: %s", client.nick, modeStr.String())
			} else {
				// Set user modes (only own modes)
				if !strings.EqualFold(target, client.nick) {
					send(conn, ":%s %s %s :Cannot change mode for other users\n",
						s.getConfig().Server.Host, ErrUsersDontMatch, client.nick)
					return true
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
	} else if cmd == SetPassCmd && client.registered {
		// Command: SETPASS <oldpassword> <newpassword>
		// Change password for authenticated account
		log.Infof("Processing SETPASS command")

		if !client.authenticated {
			log.Infof("SETPASS: User '%s' not authenticated", client.nick)
			send(conn, ":%s %s :You must be logged in to change your password\n",
				s.getConfig().Server.Host, ErrAccountRequired)
			return true
		}

		parts := strings.Fields(args)
		if len(parts) < 2 {
			log.Infof("SETPASS: Not enough parameters")
			send(conn, ":%s %s %s :Not enough parameters (usage: SETPASS <oldpassword> <newpassword>)\n",
				s.getConfig().Server.Host, ErrNeedMoreParams, SetPassCmd)
			return true
		}

		oldPassword := parts[0]
		newPassword := parts[1]

		log.Infof("SETPASS: User '%s' attempting password change", client.accountName)

		// Validate new password strength
		if !IsValidPassword(newPassword) {
			log.Infof("SETPASS: New password too weak for user '%s'", client.accountName)
			send(conn, ":%s %s :New password does not meet security requirements (minimum 8 characters, maximum 72 characters)\n",
				s.getConfig().Server.Host, ErrWeakPassword)
			return true
		}

		if s.RedisClient != nil {
			ctx := context.Background()
			// Get user data to verify old password
			userData, err := s.RedisClient.GetUser(ctx, client.accountName)
			if err != nil {
				log.Errorf("SETPASS: Failed to get user data for '%s': %v", client.accountName, err)
				send(conn, ":%s %s :Password change failed - please try again later\n",
					s.getConfig().Server.Host, ErrNoSuchService)
				return true
			}

			// Verify old password
			if err := VerifyPassword(userData.HashedPassword, oldPassword); err != nil {
				log.Warnf("SETPASS: Old password verification failed for '%s'", client.accountName)
				send(conn, ":%s %s :Old password incorrect\n",
					s.getConfig().Server.Host, ErrPasswdMismatch)
				return true
			}

			// Hash new password
			newHashedPassword, err := HashPassword(newPassword)
			if err != nil {
				log.Errorf("SETPASS: Failed to hash new password for '%s': %v", client.accountName, err)
				send(conn, ":%s %s :Password change failed - please try again later\n",
					s.getConfig().Server.Host, ErrNoSuchService)
				return true
			}

			// Update password in Redis
			if err := s.RedisClient.UpdatePassword(ctx, client.accountName, newHashedPassword); err != nil {
				log.Errorf("SETPASS: Failed to update password for '%s': %v", client.accountName, err)
				send(conn, ":%s %s :Password change failed - please try again later\n",
					s.getConfig().Server.Host, ErrNoSuchService)
				return true
			}

			log.Infof("SETPASS: Successfully changed password for '%s'", client.accountName)
			send(conn, ":%s NOTICE %s :Password changed successfully\n",
				s.getConfig().Server.Host, client.nick)
		} else {
			log.Warn("SETPASS: Redis not available")
			send(conn, ":%s %s :Account service not available\n",
				s.getConfig().Server.Host, ErrNoSuchService)
		}
	} else if cmd == WhoAccCmd && client.registered {
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
						s.getConfig().Server.Host, client.nick, client.accountName)
				} else {
					send(conn, ":%s NOTICE %s :Account: %s\n",
						s.getConfig().Server.Host, client.nick, userData.Username)
					send(conn, ":%s NOTICE %s :Email: %s\n",
						s.getConfig().Server.Host, client.nick, userData.Email)
					send(conn, ":%s NOTICE %s :Created: %s\n",
						s.getConfig().Server.Host, client.nick, userData.CreatedAt.Format("2006-01-02 15:04:05"))
					if !userData.LastLogin.IsZero() {
						send(conn, ":%s NOTICE %s :Last Login: %s\n",
							s.getConfig().Server.Host, client.nick, userData.LastLogin.Format("2006-01-02 15:04:05"))
					}
					log.Infof("WHOACC: Displayed account info for '%s'", client.accountName)
				}
			} else {
				send(conn, ":%s NOTICE %s :Account: %s (authenticated)\n",
					s.getConfig().Server.Host, client.nick, client.accountName)
			}
		} else {
			send(conn, ":%s NOTICE %s :You are not logged in to any account\n",
				s.getConfig().Server.Host, client.nick)
			log.Infof("WHOACC: User '%s' not authenticated", client.nick)
		}
	}
	return true
}
