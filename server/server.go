package server

import (
	"bufio"
	"fmt"
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
	clientMux  sync.Mutex
}

type ClientList struct {
	clients    []Client
	clientsMux sync.Mutex
}

type Room struct {
	identifier uuid.UUID
	name       string
	topic      string
	clients    map[uuid.UUID]bool
}

const (
	// Registration commands
	PassCmd = "PASS"
	NickCmd = "NICK"
	UserCmd = "USER"
	QuitCmd = "QUIT"

	// Error commands
	ErrNoSuchService     = "408"
	ErrNoOrigin          = "409"
	ErrNickInUse         = "433"
	ErrNickInvalid       = "432"
	ErrNickNull          = "431"
	ErrNeedMoreParams    = "461" // <command> :Not enough parameters
	ErrAlreadyRegistered = "462" // :You may not reregister

	// client commands
	PrivmsgCmd = "PRIVMSG"
	NoticeCmd  = "NOTICE"
	PingCmd    = "PING"
	JoinCmd    = "JOIN"

	// server responses
	PingPongCmd = "PONG"

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
var lobby *Room
var roomList []Room

func InitClient(conn net.Conn) {
	go clientHandleConnect(conn)
}

func InitServer() {
	initClientList()
	initRoomList()
}

func initRoomList() {
	// create lobby
	roomID, err := uuid.NewRandom()
	if err != nil {
		log.Fatal("Could not create node identifier")
	}
	lobby = &Room{roomID, "lobby", "", make(map[uuid.UUID]bool)}
	roomList = append(roomList, *lobby)
}

func initClientList() {
	connectedClientList = *new(ClientList)
}

func clientHandleConnect(conn net.Conn) {
	fmt.Printf("Client connecting, handling connection ...\n")
	clientIdentifier, err := uuid.NewRandom()
	if err != nil {
		log.Fatal("Could not create node identifier")
	}
	fmt.Printf("This is concurrent user #%d, generated UUID is: %s\n", len(connectedClientList.clients), clientIdentifier)
	quit := false
	pass := false
	initHappened := false
	// error := false

	newClient := new(Client)
	newClient.identifier = clientIdentifier
	newClient.conn = conn
	newClient.rooms = make(map[uuid.UUID]bool)

	defer func() { // called when the function ends
		fmt.Printf("Client quit or inactive, closing connection ...\n")
		connectedClientList.remove(newClient.identifier)
		conn.Close()
	}()

	timeoutDuration := 15 * time.Minute
	for {
		quit = false

		// Set a deadline for reading. Read operation will fail if no data
		// is received after deadline.
		conn.SetReadDeadline(time.Now().Add(timeoutDuration))

		message, err := bufio.NewReader(conn).ReadString('\n')

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
			} else if cmd == PassCmd && pass {
				clientReadConnectPass(args)
				pass = false
				continue

			} else if cmd == NickCmd && len(args) != 0 && !pass {
				// Command: NICK Parameters: <nickname> [ <hopcount> ]
				if len(args) == 0 {
					errMsg := fmt.Sprintf(":%s %s :No nickname given\n", serverHost, ErrNickNull)
					fmt.Printf(">> %s", errMsg)
					conn.Write([]byte(errMsg))
				} else if connectedClientList.contains(args) {
					errMsg := fmt.Sprintf(":%s %s * %s :Nickname is already in use\n", serverHost, ErrNickInUse, args)
					fmt.Printf(">> %s", errMsg)
					conn.Write([]byte(errMsg))
				} else if !validCharset(args) {
					errMsg := fmt.Sprintf(":%s %s * %s :Erroneous nickname\n", serverHost, ErrNickInvalid, args)
					fmt.Printf(">> %s", errMsg)
					conn.Write([]byte(errMsg))
				} else {
					newClient.nick = args
					newClient.user = "*"
					clientInit(newClient)
					initHappened = true
				}
			} else if cmd == UserCmd && len(args) != 0 && !pass {
				// Command: USER Parameters: <user> <mode> <unused> <realname>
				// Example: USER shout-user 0 * :Shout User
				if len(args) == 0 {
					errMsg := fmt.Sprintf(":%s %s :You may not reregister\n", serverHost, ErrAlreadyRegistered)
					fmt.Printf(">> %s", errMsg)
					conn.Write([]byte(errMsg))
				} else {
					split := strings.Split(args, ":")
					if len(split) < 2 {
						errMsg := fmt.Sprintf(":%s %s USER :Not enough parameters\n", serverHost, ErrNeedMoreParams)
						fmt.Printf(">> %s", errMsg)
						conn.Write([]byte(errMsg))
					} else if len(strings.Split(split[0], " ")) < 3 {
						errMsg := fmt.Sprintf(":%s %s USER :Not enough parameters\n", serverHost, ErrNeedMoreParams)
						fmt.Printf(">> %s", errMsg)
						conn.Write([]byte(errMsg))
					} else {
						args1 := split[0]
						args2 := split[1]

						newClient.user = strings.Split(args1, " ")[0]
						newClient.mode, _ = strconv.Atoi(strings.Split(args1, " ")[1])
						newClient.unused = strings.Split(args1, " ")[2]
						newClient.realname = args2
					}
				}
			} else if cmd == PingCmd && len(args) != 0 && initHappened {
				if len(args) == 0 {
					errMsg := fmt.Sprintf(":%s %s :No origin specified\n", serverHost, ErrNoOrigin)
					fmt.Printf(">> %s", errMsg)
					conn.Write([]byte(errMsg))
				} else {
					origins := strings.Split(args, " ")
					if len(origins) == 1 {
						answerMsg := fmt.Sprintf(":%s %s %s\n", serverHost, PingPongCmd, args)
						fmt.Printf(">> %s", answerMsg)
						conn.Write([]byte(answerMsg))
					} else {
						// query other server as well for now just answer
						// ...
						answerMsg := fmt.Sprintf(":%s %s %s\n", serverHost, PingPongCmd, args)
						fmt.Printf(">> %s", answerMsg)
						conn.Write([]byte(answerMsg))
					}
				}
			} else if cmd == JoinCmd && len(args) != 0 && initHappened {

			} else if cmd == QuitCmd {
				quit = true
			}
		}

		if quit {
			break
		}
	}
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

	welcomeMessage := fmt.Sprintf(":%s %s :Welcome to 'girc v%s' %s!%s@%s\n", serverHost, RplWelcome, Version(), client.nick, client.user, serverHost)
	fmt.Printf(">> %s", welcomeMessage)
	client.conn.Write([]byte(welcomeMessage))

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
	for _, r := range roomList {
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
		room = &Room{roomID, roomName, "", make(map[uuid.UUID]bool)}
		roomList = append(roomList, *room)
	}
	room.clients[client.identifier] = true
	client.rooms[room.identifier] = true
	// send to all existing clients + user, that user has joined:
	message := fmt.Sprintf(":%s!%s@%s %s #%s\n", client.nick, client.user, serverHost, "JOIN", room.name)
	fmt.Printf(">> %s", message)
	var names []string
	for ident, _ := range room.clients {
		for _, cli := range connectedClientList.clients {
			if ident == cli.identifier {
				cli.conn.Write([]byte(message))
				names = append(names, cli.nick)
			}
		}
	}

	if room.topic == "" {
		message = fmt.Sprintf(":%s %s %s #%s :No topic is set\n", serverHost, RplTopic, client.nick, room.name)
	} else {
		message = fmt.Sprintf(":%s %s %s #%s :%s\n", serverHost, RplNoTopic, client.nick, room.name, room.topic)
	}
	fmt.Printf(">> %s", message)
	client.conn.Write([]byte(message))

	// send list of all clients in room to user
	// "( "=" / "*" / "@" ) <channel> :[ "@" / "+" ] <nick> *( " " [ "@" / "+" ] <nick> )
	message = fmt.Sprintf(":%s %s %s = #%s :%s\n", serverHost, RplNameReply, client.nick, room.name, strings.Join(names[:], " "))
	fmt.Printf(">> %s", message)
	client.conn.Write([]byte(message))
	message = fmt.Sprintf(":%s %s %s #%s :End of NAMES list\n", serverHost, RplEndOfNames, client.nick, room.name)
	fmt.Printf(">> %s", message)
	client.conn.Write([]byte(message))
}

func (cl *ClientList) add(client Client) {
	cl.clientsMux.Lock()
	defer cl.clientsMux.Unlock()
	cl.clients = append(cl.clients, client)
}
func (cl *ClientList) remove(ident uuid.UUID) {
	cl.clientsMux.Lock()
	defer cl.clientsMux.Unlock()

	if len(cl.clients) == 1 {
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
			cl.clients = cl.clients[:index]
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

func validCharset(test string) bool {
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

func Version() string {
	return fmt.Sprintf("%d.%d.%d", serverMajorVersion, serverMinorVersion, serverPatchVersion)
}
