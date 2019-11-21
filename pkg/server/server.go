package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/tehcyx/girc/internal/config"
	"github.com/tehcyx/girc/pkg/version"
)

type Server struct {
	Clients      []Client
	ClientsMutex *sync.Mutex

	Rooms      []Room
	RoomsMutex *sync.Mutex

	Lobby *Room

	ClientTimeout time.Duration
}

//New creates a new server, listening on the defined port and starting to accept client connections
func New() {
	log.Println("Launching server...")

	// listen on all interfaces
	ln, err := net.Listen("tcp", fmt.Sprintf(":%s", config.Values.Server.Port))
	if err != nil {
		log.Error(fmt.Errorf("listen failed, port possibly in use already: %w", err))
	}

	defer func() {
		log.Printf("Shutting down server. Bye!\n")
		ln.Close()
	}()

	srv := &Server{
		Clients:      []Client{},
		ClientsMutex: &sync.Mutex{},

		Rooms:      []Room{},
		RoomsMutex: &sync.Mutex{},

		ClientTimeout: 15 * time.Minute,
	}

	srv.initRooms()

	// run loop forever (or until ctrl-c)
	for {
		// accept connection on port
		conn, _ := ln.Accept()

		go srv.handleClientConnect(conn)
	}
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
	log.Debugf("This is concurrent user #%d, generated UUID is: %s\n", len(s.Clients), identifier)

	pass := false
	initCompleted := false

	client := Client{}
	client.clientMux = &sync.Mutex{}
	client.identifier = identifier
	client.conn = conn
	client.rooms = make(map[uuid.UUID]bool)

	defer func() {
		s.RemoveClient(client.identifier)

		_, err := conn.Read(make([]byte, 0))
		if err != io.EOF {
			// this connection is invalid
			fmt.Printf("Client hung up unexpectedly or quit ...\n")
		} else {
			fmt.Printf("Client quit or inactive, closing connection ...\n")
			conn.Close()
		}
	}()

	for {
		// Set a deadline for reading. Read operation will fail if no data
		// is received after deadline.
		conn.SetReadDeadline(time.Now().Add(s.ClientTimeout))

		reader := bufio.NewReader(conn)
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
			continue
		} else {
			log.Debugf("Received message from client: %s", message)
			message = strings.TrimSpace(message)
			tokens := strings.Split(message, " ")

			cmd := tokens[0]
			args := strings.Join(tokens[1:], " ")

			if len(cmd) == 0 {
				continue
			} else if cmd == QuitCmd {
				// send goodbye message
				return
			} else if cmd == PassCmd && pass {
				//clientReadConnectPass(args)
				pass = false
				continue
			} else if cmd == NickCmd && !pass {
				// Command: NICK Parameters: <nickname> [ <hopcount> ]
				if len(args) == 0 {
					send(conn, ":%s %s :No nickname given\n", config.Values.Server.Host, ErrNickNull)
				} else if s.ClientExists(args) {
					send(conn, ":%s %s * %s :Nickname is already in use\n", config.Values.Server.Host, ErrNickInUse, args)
				} else if !validNickCharset(args) {
					send(conn, ":%s %s * %s :Erroneous nickname\n", config.Values.Server.Host, ErrNickInvalid, args)
				} else {
					client.nick = args
					client.user = "*"
					continue
				}
			} else if cmd == UserCmd && !pass {
				// Command: USER Parameters: <user> <mode> <unused> <realname>
				// Example: USER shout-user 0 * :Shout User
				if len(args) == 0 {
					send(conn, ":%s %s %s:You may not reregister\n", config.Values.Server.Host, ErrAlreadyRegistered, client.nick)
				} else {
					split := strings.Split(args, ":")
					if len(split) < 2 || len(strings.Split(split[0], " ")) < 3 {
						send(conn, ":%s %s %s USER :Not enough parameters\n", config.Values.Server.Host, ErrNeedMoreParams, client.nick)
					} else {
						args1 := split[0]
						args2 := split[1]

						client.user = strings.Split(args1, " ")[0]
						client.mode, _ = strconv.Atoi(strings.Split(args1, " ")[1])
						client.unused = strings.Split(args1, " ")[2]
						client.realname = args2
						s.AddClient(client)
						initCompleted = true
					}
				}
			} else if cmd == PingCmd && initCompleted {
				if len(args) == 0 {
					send(conn, ":%s %s :No origin specified\n", config.Values.Server.Host, ErrNoOrigin)
				} else {
					origins := strings.Split(args, " ")
					if len(origins) == 1 {
						send(conn, ":%s %s %s\n", config.Values.Server.Host, PingPongCmd, args)
					} else {
						// query other server as well for now just answer
						// ...
						send(conn, ":%s %s %s\n", config.Values.Server.Host, PingPongCmd, args)
					}
				}
			} else if cmd == JoinCmd && initCompleted {
				if len(args) == 0 {
					send(conn, ":%s %s %s :Not enough parameters\n", config.Values.Server.Host, ErrNeedMoreParams, JoinCmd)
				} else {
					rooms := strings.Split(args, ",")
					for _, roomName := range rooms {
						s.JoinRoomByName(client, roomName)
					}
				}
			} else if cmd == PrivmsgCmd && initCompleted {
				if len(args) == 0 {
					send(conn, ":%s %s :No recipient given\n", config.Values.Server.Host, ErrNoRecipient)
				} else {
					parts := strings.Split(args, ":")
					target := strings.TrimSpace(parts[0])
					chatMessage := strings.TrimSpace(parts[1])
					if len(parts) != 2 {
						send(conn, ":%s %s :No text to send\n", config.Values.Server.Host, ErrNoTextToSend)
					}
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
				}
			} else if cmd == PartCmd && initCompleted {
				if len(args) == 0 {
					send(conn, ":%s %s :No recipient given\n", config.Values.Server.Host, ErrNoRecipient)
				} else {
					parts := strings.Split(args, ":")
					partTargets := strings.Split(strings.TrimSpace(parts[0]), ",")
					partMessage := strings.TrimSpace(parts[1])

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
				}
			}
		}
	}
}

func (s *Server) AddClient(client Client) error {
	s.ClientsMutex.Lock()
	s.Clients = append(s.Clients, client)
	s.ClientsMutex.Unlock()

	send(client.conn, ":%s %s %s :Welcome to the Internet Relay Network %s!%s@%s\n", config.Values.Server.Host, RplWelcome, client.nick, client.nick, client.user, config.Values.Server.Host)
	send(client.conn, ":%s %s %s :Your host is %s, running version %s\n", config.Values.Server.Host, RplYourHost, client.nick, config.Values.Server.Host, version.Version)
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

	var room Room
	s.RoomsMutex.Lock()
	for _, r := range s.Rooms {
		if strings.Compare(r.name, roomName) == 0 {
			room = r
			break
		}
	}
	if room.name == "" {
		room = createRoom(roomName)
		s.Rooms = append(s.Rooms, room)
	}
	s.RoomsMutex.Unlock()

	room.roomMux.Lock()
	room.clients[client.identifier] = true
	room.roomMux.Unlock()

	client.clientMux.Lock()
	client.rooms[room.identifier] = true
	client.clientMux.Unlock()

	// send to all existing clients + user, that user has joined:
	var names []string
	for ident := range room.clients {
		for _, cli := range s.Clients {
			if ident == cli.identifier {
				send(cli.conn, ":%s!%s@%s %s #%s\n", client.nick, client.user, config.Values.Server.Host, JoinCmd, room.name)
				names = append(names, cli.nick)
			}
		}
	}

	if room.topic == "" {
		send(client.conn, ":%s %s %s #%s :No topic is set\n", config.Values.Server.Host, RplTopic, client.nick, room.name)
	} else {
		send(client.conn, ":%s %s %s #%s :%s\n", config.Values.Server.Host, RplNoTopic, client.nick, room.name, room.topic)
	}

	// send list of all clients in room to user
	// "( "=" / "*" / "@" ) <channel> :[ "@" / "+" ] <nick> *( " " [ "@" / "+" ] <nick> )
	send(client.conn, ":%s %s %s = #%s :%s\n", config.Values.Server.Host, RplNameReply, client.nick, room.name, strings.Join(names[:], " "))

	send(client.conn, ":%s %s %s #%s :End of NAMES list\n", config.Values.Server.Host, RplEndOfNames, client.nick, room.name)
	return nil
}

func (s *Server) RemoveClient(id uuid.UUID) {
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
			s.Clients = s.Clients[index:]
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

func createRoom(name string) Room {
	return Room{
		roomMux:    &sync.Mutex{},
		identifier: uuid.Must(uuid.NewRandom()),
		name:       name,
		clients:    make(map[uuid.UUID]bool),
		topic:      "",
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
