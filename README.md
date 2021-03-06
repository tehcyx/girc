```none
 .o88b. d888888b d8888b.  .o88b. 
d8P       `88'   88  `8D d8P  Y8 
8P         88    88oobY' 8P      
8b  d8b    88    88`8b   8b      
Y8b  88   .88.   88 `88. Y8b  d8 
 `Y88P' Y888888P 88   YD  `Y88P' 
```

# girc
IRC server written in Golang.

[circ](https://github.com/tehcyx/circ) is trying to do the same with C, but I have to update the protocol version.

## Test Clients
I'm currently using [Colloquy](http://colloquy.info/) and [Shout](http://shout-irc.com/) to test my functionality.

# Feature List
## Currently (Partially) Supported Commands
* Messaging [Sending messages](https://tools.ietf.org/html/rfc1459#section-4.4)
  * `PRIVMSG tehcyx hello tehcyx` [Private messages](https://tools.ietf.org/html/rfc2812#section-3.3.1)
  * `NOTICE tehcyx hello tehcyx` [Notice](https://tools.ietf.org/html/rfc2812#section-3.3.2)
* Connection registration [Connection registration](https://tools.ietf.org/html/rfc2812#section-3.1)
  * `PASS secretpass` [Password message](https://tools.ietf.org/html/rfc2812#section-3.1.1)
  * `NICK tehcyx [ <hopcount> ]` [Nick message](https://tools.ietf.org/html/rfc2812#section-3.1.2) - hopcount actually not yet supported
  * `USER <user> <mode> <unused> <realname>` [User message](https://tools.ietf.org/html/rfc2812#section-3.1.3)
  * `QUIT [<Quit message>]` [Quit](https://tools.ietf.org/html/rfc2812#section-3.1.7) - Quit message not supported
* Channel operations [Channel operations](https://tools.ietf.org/html/rfc1459#section-4.2)
  * `JOIN <channel>{,<channel>} [<key>{,<key>}]` [Join message](https://tools.ietf.org/html/rfc1459#section-4.2.1)
  * `PART <channel>{,<channel>}` [Part message](https://tools.ietf.org/html/rfc1459#section-4.2.2)

## What's next
* Connection registration [Connection registration](https://tools.ietf.org/html/rfc1459#section-4.1)
  * `OPER <user> <password>` [Oper](https://tools.ietf.org/html/rfc1459#section-4.1.5)
* Channel operations [Channel operations](https://tools.ietf.org/html/rfc1459#section-4.2)
  * `MODE` [Mode message](https://tools.ietf.org/html/rfc1459#section-4.2.3)
    * `<channel> {[+|-]|o|p|s|i|t|n|b|v} [<limit>] [<user>] [<ban mask>]` [Channel modes](https://tools.ietf.org/html/rfc1459#section-4.2.3.1)
    * `<nickname> {[+|-]|i|w|s|o}` [User modes](https://tools.ietf.org/html/rfc1459#section-4.2.3.2)
  * `TOPIC <channel> [<topic>]` [Topic message](https://tools.ietf.org/html/rfc1459#section-4.2.4)

## And after that
* `SERVER <servername> <hopcount> <info>` [Server message](https://tools.ietf.org/html/rfc1459#section-4.1.4)
* `SQUIT <server> <comment>` [Server quit message](https://tools.ietf.org/html/rfc1459#section-4.1.7)

# RFC
* [RFC2812](https://tools.ietf.org/html/rfc2812)

# Inspiring reads
* [IRC Examples](http://chi.cs.uchicago.edu/chirc/irc_examples.html)
* [Modern IRC ...](https://modern.ircdocs.horse/)
* [Go Concurrency](https://tour.golang.org/concurrency/9)
* [Go Testing](https://golang.org/pkg/testing/)
* [Go Package Names](https://blog.golang.org/package-names)
* [Go Documentation](https://blog.golang.org/godoc-documenting-go-code)