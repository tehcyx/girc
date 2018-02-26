package server

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const serverMajorVersion = 0
const serverMinorVersion = 0
const serverPatchVersion = 1

// const serverName = "girc.rothdaniel.de"
const serverName = "girc.rothdaniel.de"
const serverHost = "localhost"

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

// ClientList struct holding a []Client and a sync.Mutex to modify the array.
type ClientList struct {
	clients    []Client
	clientsMux *sync.Mutex
}

// Room struct holding info about created a room.
type Room struct {
	identifier uuid.UUID
	name       string
	topic      string
	clients    map[uuid.UUID]bool
	roomMux    *sync.Mutex
}

// RoomsList struct holding a []Room and a sync.Mutex to modify the array.
type RoomsList struct {
	list    []Room
	listMux *sync.Mutex
}

const (
	// Registration commands
	// PassCmd `PASS secretpass` [Password message](https://tools.ietf.org/html/rfc2812#section-3.1.1)
	PassCmd = "PASS"
	// NickCmd `NICK tehcyx [ <hopcount> ]` [Nick message](https://tools.ietf.org/html/rfc2812#section-3.1.2) - hopcount actually not yet supported
	NickCmd = "NICK"
	// UserCmd `USER <user> <mode> <unused> <realname>` [User message](https://tools.ietf.org/html/rfc2812#section-3.1.3)
	UserCmd = "USER"
	// QuitCmd `QUIT [<Quit message>]` [Quit](https://tools.ietf.org/html/rfc2812#section-3.1.7) - Quit message not
	QuitCmd = "QUIT"
	// !Registration commands

	// Error commands
	ErrNoSuchNick        = "401"
	ErrNoSuchChannel     = "403"
	ErrCannotSendToChan  = "404"
	ErrNoSuchService     = "408"
	ErrNoOrigin          = "409"
	ErrNoRecipient       = "411"
	ErrNoTextToSend      = "412"
	ErrNickInUse         = "433"
	ErrNickInvalid       = "432"
	ErrNickNull          = "431"
	ErrNotOnChannel      = "442"
	ErrNeedMoreParams    = "461" // <command> :Not enough parameters
	ErrAlreadyRegistered = "462" // :You may not reregister
	// !Error commands

	// Client commands
	PrivmsgCmd = "PRIVMSG"
	NoticeCmd  = "NOTICE"
	PingCmd    = "PING"
	JoinCmd    = "JOIN"
	PartCmd    = "PART"
	// !Client commands

	// Server responses
	PingPongCmd = "PONG"
	// !Server responses

	// Command Responses
	RplWelcome    = "001"
	RplYourHost   = "002"
	RplCreated    = "003"
	RplMyInfo     = "004"
	RplBounce     = "005"
	RplNameReply  = "353"
	RplEndOfNames = "366"
	RplNoTopic    = "331"
	RplTopic      = "332"
)

var connectedClientList ClientList
var rooms RoomsList
var lobby *Room

// InitClient initializes a client connection to run in a different thread.
func InitClient(conn net.Conn) {
	go clientHandleConnect(conn)
}

// InitServer initializes the server, creating an empty list of clients
// and a room list that contains just the lobby for now.
func InitServer() {
	initClientList()
	initRoomList()
}

func initRoomList() {
	rooms = *new(RoomsList)
	rooms.listMux = &sync.Mutex{}
	rooms.list = []Room{}
	rooms.listMux.Lock()
	defer rooms.listMux.Unlock()

	// create lobby
	roomID, err := uuid.NewRandom()
	if err != nil {
		log.Fatal("Could not create node identifier")
	}
	lobby = &Room{}
	lobby.roomMux = &sync.Mutex{}
	lobby.identifier = roomID
	lobby.name = "lobby"
	lobby.clients = make(map[uuid.UUID]bool)
	lobby.topic = ""
	rooms.list = append(rooms.list, *lobby)
}

func initClientList() {
	connectedClientList = *new(ClientList)
	connectedClientList.clientsMux = &sync.Mutex{}
}

func clientHandleConnect(conn net.Conn) {
	fmt.Printf("Client connecting, handling connection ...\n")
	clientIdentifier, err := uuid.NewRandom()
	if err != nil {
		log.Fatal("Could not create node identifier")
	}
	fmt.Printf("This is concurrent user #%d, generated UUID is: %s\n", len(connectedClientList.clients), clientIdentifier)
	pass := false
	initHappened := false
	// error := false

	newClient := new(Client)
	newClient.clientMux = &sync.Mutex{}
	newClient.identifier = clientIdentifier
	newClient.conn = conn
	newClient.rooms = make(map[uuid.UUID]bool)

	defer func() { // called when the function ends
		if newClient != nil {
			connectedClientList.remove(newClient.identifier)
		}
		_, err := conn.Read(make([]byte, 0))
		if err != io.EOF {
			// this connection is invalid
			fmt.Printf("Client hung up unexpectedly or quit ...\n")
		} else {
			fmt.Printf("Client quit or inactive, closing connection ...\n")
			conn.Close()
		}
	}()

	timeoutDuration := 15 * time.Minute
	for {
		// Set a deadline for reading. Read operation will fail if no data
		// is received after deadline.
		conn.SetReadDeadline(time.Now().Add(timeoutDuration))

		reader := bufio.NewReader(conn)
		// TODO https://twitter.com/davecheney/status/604837853344989184?lang=en
		message, err := reader.ReadString('\n') // there's a problem with clients that send multiple lines, will have to figure out a solution using the scanner API

		if err != nil {
			fmt.Printf("An error occured, now closing connection to client socket. Error: %s\n", err)
			return
		} else if len(message) == 0 {
			continue
		} else {
			fmt.Printf("Received message from client: %s", message)
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
				clientReadConnectPass(args)
				pass = false
				continue
			} else if cmd == NickCmd && !pass {
				// Command: NICK Parameters: <nickname> [ <hopcount> ]
				if len(args) == 0 {
					send(conn, ":%s %s :No nickname given\n", serverHost, ErrNickNull)
				} else if connectedClientList.contains(args) {
					send(conn, ":%s %s * %s :Nickname is already in use\n", serverHost, ErrNickInUse, args)
				} else if !validNickCharset(args) {
					send(conn, ":%s %s * %s :Erroneous nickname\n", serverHost, ErrNickInvalid, args)
				} else {
					newClient.nick = args
					newClient.user = "*"
					continue
				}
			} else if cmd == UserCmd && !pass {
				// Command: USER Parameters: <user> <mode> <unused> <realname>
				// Example: USER shout-user 0 * :Shout User
				if len(args) == 0 {
					send(conn, ":%s %s %s:You may not reregister\n", serverHost, ErrAlreadyRegistered, newClient.nick)
				} else {
					split := strings.Split(args, ":")
					if len(split) < 2 || len(strings.Split(split[0], " ")) < 3 {
						send(conn, ":%s %s %s USER :Not enough parameters\n", serverHost, ErrNeedMoreParams, newClient.nick)
					} else {
						args1 := split[0]
						args2 := split[1]

						newClient.user = strings.Split(args1, " ")[0]
						newClient.mode, _ = strconv.Atoi(strings.Split(args1, " ")[1])
						newClient.unused = strings.Split(args1, " ")[2]
						newClient.realname = args2
						clientInit(newClient)
						initHappened = true
					}
				}
			} else if cmd == PingCmd && initHappened {
				if len(args) == 0 {
					send(conn, ":%s %s :No origin specified\n", serverHost, ErrNoOrigin)
				} else {
					origins := strings.Split(args, " ")
					if len(origins) == 1 {
						send(conn, ":%s %s %s\n", serverHost, PingPongCmd, args)
					} else {
						// query other server as well for now just answer
						// ...
						send(conn, ":%s %s %s\n", serverHost, PingPongCmd, args)
					}
				}
			} else if cmd == JoinCmd && initHappened {
				if len(args) == 0 {
					send(conn, ":%s %s %s :Not enough parameters\n", serverHost, ErrNeedMoreParams, JoinCmd)
				} else {
					rooms := strings.Split(args, ",")
					for _, roomName := range rooms {
						clientJoinRoom(newClient, roomName)
					}
				}
			} else if cmd == PrivmsgCmd && initHappened {
				if len(args) == 0 {
					send(conn, ":%s %s :No recipient given\n", serverHost, ErrNoRecipient)
				} else {
					parts := strings.Split(args, ":")
					target := strings.TrimSpace(parts[0])
					chatMessage := strings.TrimSpace(parts[1])
					if len(parts) != 2 {
						send(conn, ":%s %s :No text to send\n", serverHost, ErrNoTextToSend)
					}
					if strings.HasPrefix(target, "#") || strings.HasPrefix(target, "$") {
						// is channel or mask
						if validMessageMask(target) {

							fmt.Printf("Masking is not implemented, so we just swallow that message: %s\n", args)
							// ERR_WILDTOPLEVEL
							// ERR_NOTOPLEVEL
							// ERR_TOOMANYTARGETS
						} else {
							if inChannel, room := isUserInChannel(newClient, target); inChannel {
								room.roomMux.Lock()
								for ident := range room.clients {
									if ident == newClient.identifier {
										continue
									}
									for _, cli := range connectedClientList.clients {
										if ident == cli.identifier {
											send(cli.conn, ":%s!%s@%s %s %s :%s\n", newClient.nick, newClient.user, serverHost, PrivmsgCmd, target, chatMessage)
										}
									}
								}
								room.roomMux.Unlock()
							} else {
								send(conn, ":%s %s %s %s :Cannot send to channel\n", serverHost, ErrCannotSendToChan, newClient.nick, target)
							}
						}
					} else if validNickCharset(target) {
						// not a channel or mask, let's find out if the requested user is connected
						targetClient := connectedClientList.getClientByName(target)
						if targetClient == nil {
							send(conn, ":%s %s %s %s :No such nick/channel\n", serverHost, ErrNoSuchNick, newClient.nick, target)
						} else {
							send(targetClient.conn, ":%s!%s@%s %s %s :%s\n", newClient.nick, newClient.user, serverHost, PrivmsgCmd, target, chatMessage)
						}
					}
				}
			} else if cmd == PartCmd && initHappened {
				if len(args) == 0 {
					send(conn, ":%s %s :No recipient given\n", serverHost, ErrNoRecipient)
				} else {
					parts := strings.Split(args, ":")
					partTargets := strings.Split(strings.TrimSpace(parts[0]), ",")
					partMessage := strings.TrimSpace(parts[1])

					for _, target := range partTargets {
						if channelExists(target) {
							if inChannel, room := isUserInChannel(newClient, target); inChannel {
								room.roomMux.Lock()
								defer room.roomMux.Unlock()
								for ident := range room.clients {
									fmt.Printf("exists %d, %s, %s, %s\n", 2, room.name, ident, newClient.identifier)
									if ident != newClient.identifier {
										continue
									}
									for _, cli := range connectedClientList.clients {
										send(cli.conn, ":%s!%s@%s %s %s :%s\n", newClient.nick, newClient.user, serverHost, PartCmd, target, partMessage)
										if ident == cli.identifier {
											delete(room.clients, newClient.identifier) // remove user from room
											newClient.clientMux.Lock()
											defer newClient.clientMux.Unlock()
											delete(newClient.rooms, room.identifier) // remove room from users rooms list
										}
									}
								}
							} else {
								send(conn, ":%s %s %s :You're not on that channel\n", serverHost, ErrNotOnChannel, target)
							}
						} else {
							send(conn, ":%s %s %s :No such channel\n", serverHost, ErrNoSuchChannel, target)
						}
					}
				}
			}
		}
	}
}

func channelExists(roomName string) bool {
	rooms.listMux.Lock()
	defer rooms.listMux.Unlock()

	if strings.HasPrefix(roomName, "#") {
		roomName = roomName[1:]
	}

	for _, room := range rooms.list {
		if strings.Compare(room.name, roomName) == 0 {
			return true
		}
	}
	return false
}

func send(conn net.Conn, format string, a ...interface{}) {
	message := fmt.Sprintf(format, a...)
	fmt.Printf(">> %s", message)
	conn.Write([]byte(message))
}

func clientReadConnectPass(args string) string {
	// char* client_read_connect_pass(char* cmd, char* args, bool* quit) {
	// 	// hello

	// 	// 461 %s %s :Not enough parameters

	// 	// 464 :Password incorrect
	// 	return NULL;
	// }
	return ""
}

func clientInit(client *Client) {
	connectedClientList.add(*client)

	send(client.conn, ":%s %s %s :Welcome to the Internet Relay Network %s!%s@%s\n", serverHost, RplWelcome, client.nick, client.nick, client.user, serverHost)
	send(client.conn, ":%s %s %s :Your host is %s, running version %s\n", serverHost, RplYourHost, client.nick, serverHost, Version())
	send(client.conn, ":%s %s %s :This server was created %s\n", serverHost, RplCreated, client.nick, time.Now().String())

	clientJoinRoom(client, lobby.name)
}

func clientJoinRoom(client *Client, roomName string) {
	if len(roomName) == 0 {
		return
	}
	if strings.ContainsAny(roomName, "#") {
		roomName = roomName[1:]
	}

	var room *Room
	rooms.listMux.Lock()
	defer rooms.listMux.Unlock()
	for _, r := range rooms.list {
		if strings.Compare(r.name, roomName) == 0 {
			room = &r
			break
		}
	}
	if room == nil {
		roomID, err := uuid.NewRandom()
		if err != nil {
			log.Fatal("Could not create node identifier")
		}
		room = &Room{}
		room.roomMux = &sync.Mutex{}
		room.identifier = roomID
		room.name = roomName
		room.clients = make(map[uuid.UUID]bool)
		room.topic = ""
		rooms.list = append(rooms.list, *room)
	}

	room.roomMux.Lock()
	defer room.roomMux.Unlock()

	room.clients[client.identifier] = true
	client.rooms[room.identifier] = true
	// send to all existing clients + user, that user has joined:
	var names []string
	for ident := range room.clients {
		for _, cli := range connectedClientList.clients {
			if ident == cli.identifier {
				send(cli.conn, ":%s!%s@%s %s #%s\n", client.nick, client.user, serverHost, JoinCmd, room.name)
				names = append(names, cli.nick)
			}
		}
	}

	if room.topic == "" {
		send(client.conn, ":%s %s %s #%s :No topic is set\n", serverHost, RplTopic, client.nick, room.name)
	} else {
		send(client.conn, ":%s %s %s #%s :%s\n", serverHost, RplNoTopic, client.nick, room.name, room.topic)
	}

	// send list of all clients in room to user
	// "( "=" / "*" / "@" ) <channel> :[ "@" / "+" ] <nick> *( " " [ "@" / "+" ] <nick> )
	send(client.conn, ":%s %s %s = #%s :%s\n", serverHost, RplNameReply, client.nick, room.name, strings.Join(names[:], " "))

	send(client.conn, ":%s %s %s #%s :End of NAMES list\n", serverHost, RplEndOfNames, client.nick, room.name)
}

func (cl *ClientList) add(client Client) {
	cl.clientsMux.Lock()
	defer cl.clientsMux.Unlock()

	cl.clients = append(cl.clients, client)
}
func (cl *ClientList) remove(ident uuid.UUID) {
	cl.clientsMux.Lock()
	defer cl.clientsMux.Unlock()

	if len(cl.clients) == 1 || len(cl.clients) == 0 {
		cl.clients = []Client{}
	} else {
		var index int
		for num, iter := range cl.clients {
			if iter.identifier == ident {
				index = num
				break
			}
		}
		next := index + 1
		if len(cl.clients) == 2 {
			if index == 0 {
				cl.clients = cl.clients[index:]
			} else {
				cl.clients = cl.clients[:index]
			}
		} else {
			cl.clients = append(cl.clients[:index], cl.clients[next:]...)
		}
	}
}

func (cl *ClientList) contains(nick string) bool {
	cl.clientsMux.Lock()
	defer cl.clientsMux.Unlock()

	for _, client := range cl.clients {
		if strings.Compare(client.nick, nick) == 0 {
			return true
		}
	}
	return false
}

func (cl *ClientList) getClientByName(nick string) *Client {
	cl.clientsMux.Lock()
	defer cl.clientsMux.Unlock()

	for _, client := range cl.clients {
		if strings.Compare(client.nick, nick) == 0 {
			return &client
		}
	}
	return nil
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

func isUserInChannel(client *Client, roomName string) (bool, *Room) {
	compareRoomName := roomName
	if strings.HasPrefix(roomName, "#") {
		compareRoomName = roomName[1:]
	}
	rooms.listMux.Lock()
	defer rooms.listMux.Unlock()

	for _, room := range rooms.list {
		if strings.Compare(room.name, compareRoomName) == 0 {
			if _, ok := client.rooms[room.identifier]; ok {
				return true, &room
			}
		}
	}
	return false, nil
}

// Version returns the version number
func Version() string {
	return fmt.Sprintf("%d.%d.%d", serverMajorVersion, serverMinorVersion, serverPatchVersion)
}
