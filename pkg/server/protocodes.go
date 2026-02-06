// Package server provides IRC protocol constants and server implementation.
// This file defines all IRC command names, error codes, and reply codes
// as specified in RFC 1459 and RFC 2812.
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
	// RegisterCmd `REGISTER <username> <password> <email>` - User registration with secure password storage
	RegisterCmd = "REGISTER"
	// SetPassCmd `SETPASS <oldpassword> <newpassword>` - Change account password
	SetPassCmd = "SETPASS"
	// WhoAccCmd `WHOACC` - Display current account information
	WhoAccCmd = "WHOACC"
	// !Registration commands

	// Error commands
	ErrNoSuchNick         = "401"
	ErrNoSuchChannel      = "403"
	ErrCannotSendToChan   = "404"
	ErrNoSuchService      = "408"
	ErrNoOrigin           = "409"
	ErrNoRecipient        = "411"
	ErrNoTextToSend       = "412"
	ErrNickInUse          = "433"
	ErrNickInvalid        = "432"
	ErrNickNull           = "431"
	ErrNotOnChannel       = "442"
	ErrUserNotInChannel   = "441" // <nick> <channel> :They aren't on that channel
	ErrUserOnChannel      = "443" // <user> <channel> :is already on channel
	ErrNeedMoreParams     = "461" // <command> :Not enough parameters
	ErrAlreadyRegistered  = "462" // :You may not reregister
	ErrPasswdMismatch     = "464" // :Password incorrect
	ErrBannedFromChan     = "474" // <channel> :Cannot join channel (+b)
	ErrChanOpPrivsNeeded  = "482" // :You're not channel operator
	ErrNoOperHost         = "491" // :No O-lines for your host
	ErrNoPrivileges       = "481" // :Permission Denied- You're not an IRC operator
	ErrUsersDontMatch     = "502" // :Cannot change mode for other users
	// !Error commands

	// Client commands
	PrivmsgCmd = "PRIVMSG"
	NoticeCmd  = "NOTICE"
	PingCmd    = "PING"
	JoinCmd    = "JOIN"
	PartCmd    = "PART"
	WhoCmd     = "WHO"
	WhoisCmd   = "WHOIS"
	TopicCmd   = "TOPIC"
	ModeCmd    = "MODE"
	ListCmd    = "LIST"
	AwayCmd    = "AWAY"
	VersionCmd   = "VERSION"
	MotdCmd      = "MOTD"
	IsonCmd      = "ISON"
	UserhostCmd  = "USERHOST"
	// !Client commands

	// Operator commands
	OperCmd    = "OPER"
	KickCmd    = "KICK"
	InviteCmd  = "INVITE"
	KillCmd    = "KILL"
	WallopsCmd = "WALLOPS"
	// !Operator commands

	// Channel Registration commands
	ChanRegCmd = "CHANREG" // Register a channel
	DropCmd    = "DROP"    // Drop a registered channel
	AccessCmd  = "ACCESS"  // Manage channel access list
	InfoCmd    = "INFO"    // Show channel registration info
	// !Channel Registration commands

	// Server responses
	PingPongCmd = "PONG"
	// !Server responses

	// Command Responses
	RplWelcome       = "001"
	RplYourHost      = "002"
	RplCreated       = "003"
	RplMyInfo        = "004"
	RplBounce        = "005"
	RplUModeIs       = "221" // <user mode string>
	RplUserhost      = "302" // :[<reply>{<space><reply>}]
	RplIson          = "303" // :<nick1> <nick2> ...
	RplUnAway        = "305" // :You are no longer marked as being away
	RplNowAway       = "306" // :You have been marked as being away
	RplWhoisUser     = "311" // <nick> <user> <host> * :<real name>
	RplWhoisServer   = "312" // <nick> <server> :<server info>
	RplWhoisOperator = "313" // <nick> :is an IRC operator
	RplEndOfWhois    = "318" // <nick> :End of WHOIS list
	RplWhoIsChannels = "319" // <nick> :<channels>
	RplChannelModeIs = "324" // <channel> <mode> <mode params>
	RplNoTopic       = "331"
	RplTopic         = "332"
	RplInviting      = "341" // <channel> <nick>
	RplList          = "322" // <channel> <# visible> :<topic>
	RplListEnd       = "323" // :End of LIST
	RplWhoReply      = "352" // <channel> <user> <host> <server> <nick> <H|G>[*][@|+] :<hopcount> <real name>
	RplEndOfWho      = "315" // <name> :End of WHO list
	RplNameReply     = "353"
	RplVersion       = "351" // <version>.<debuglevel> <server> :<comments>
	RplEndOfNames    = "366"
	RplMotdStart     = "375" // :- <server> Message of the day -
	RplMotd          = "372" // :- <text>
	RplEndOfMotd     = "376" // :End of MOTD command
	RplYoureOper     = "381" // :You are now an IRC operator
	RplLoggedIn      = "900" // <nick> <account> :You are now logged in as <account>
	RplLoggedOut     = "901" // <nick> :You are now logged out
	RplRegistered    = "902" // <nick> <username> :Account created successfully
	ErrRegisteredAlready = "903" // :Username already registered
	ErrInvalidEmail  = "904" // :Invalid email address
	ErrWeakPassword  = "905" // :Password does not meet security requirements
	ErrAccountRequired = "906" // :You must be logged in to use this command
	RplChanRegistered = "907" // <channel> :Channel registered successfully
	ErrChanRegisteredAlready = "908" // <channel> :Channel already registered
	RplChanInfo      = "909" // <channel> <field> :<value>
	RplEndOfChanInfo = "910" // <channel> :End of INFO
)
