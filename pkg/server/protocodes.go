package server

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
