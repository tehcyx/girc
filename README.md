```none
 .o88b. d888888b d8888b.  .o88b.
d8P       `88'   88  `8D d8P  Y8
8P         88    88oobY' 8P
8b  d8b    88    88`8b   8b
Y8b  88   .88.   88 `88. Y8b  d8
 `Y88P' Y888888P 88   YD  `Y88P'
```

# GIRC - IRC Server in Go

An IRC server written in Go, with optional Redis-backed distributed state management.

## Features

### Core IRC Protocol (RFC 1459/2812)
- Full connection registration (PASS, NICK, USER) — PASS is optional
- Channel operations (JOIN, PART, TOPIC, LIST, NAMES)
- Messaging (PRIVMSG, NOTICE) — colon-in-body preserved correctly
- User management (QUIT with channel broadcast)
- Server queries (WHO, WHOIS, VERSION, MOTD, ISON, USERHOST)
- MODE command for user and channel modes
- Operator commands (OPER, KICK, INVITE, WALLOPS, KILL)
- Away status (AWAY)
- Nickname change broadcasting after registration
- Server PING/PONG keepalive (90s interval, configurable)

### Optional Redis Backend
- Distributed mode: multiple pods share state via Redis pub/sub
- Cross-pod PRIVMSG, JOIN, PART, QUIT, NICK, KICK event delivery
- Channel topic and registration persistence (survives pod restart)
- User account registration and bcrypt-hashed password authentication
- Orphaned client cleanup via leader election

### Account Commands (require Redis)
| Command | Syntax | Description |
|---------|--------|-------------|
| REGISTER | `REGISTER <user> <pass> <email>` | Create account (bcrypt-stored password) |
| SETPASS | `SETPASS <oldpass> <newpass>` | Change password (must be authenticated) |
| WHOACC | `WHOACC` | Show current account info |

> **Security note**: REGISTER stores passwords using bcrypt (cost 12). The `users.yaml` operator file still uses plaintext passwords — a known limitation, see Known Limitations.

## Quick Start

### Installation

```bash
git clone https://github.com/tehcyx/girc.git
cd girc
go mod download
go build ./cmd/girc
```

### Running the Server

```bash
# Default: listens on :6667, no Redis required
./girc

# With Redis (enables distributed mode and persistence)
export REDIS_ENABLED=true
export REDIS_URL=redis://localhost:6379
./girc
```

### Connecting

```bash
# Using nc
nc localhost 6667
NICK myname
USER myname 0 * :My Name

# Using irssi
/connect localhost 6667

# Using weechat
/server add girc localhost/6667
/connect girc
```

After connecting you will receive 001–376 (welcome through end-of-MOTD).

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `IRC_PORT` | `6667` | Server listening port |
| `IRC_HOST` | `localhost` | Server hostname advertised to clients |
| `SERVER_NAME` | `girc` | Server name in IRC protocol messages |
| `MOTD_FILE` | _(none)_ | Path to a plain-text MOTD file |
| `REDIS_ENABLED` | `false` | Enable Redis backend |
| `REDIS_URL` | `redis://localhost:6379` | Redis connection URL |
| `REDIS_POD_ID` | _(auto UUID)_ | Unique pod identifier for distributed mode |
| `DEFAULT_CHANNEL_MODES` | `+nt` | Default modes applied to new channels |

All variables override values in `~/.girc/conf.yaml`. If `HOME` is unset (e.g., scratch containers), the config file is skipped and only defaults + env vars are used — no panic.

## Configuration File

`~/.girc/conf.yaml` (created automatically on first run if HOME is available):

```yaml
server:
  host: "localhost"
  port: "6667"
  name: "girc"
  motd: "Find out more on github.com/tehcyx/girc"
  debug: false
  default_channel_modes: "+nt"
redis:
  enabled: false
  url: "redis://localhost:6379"
  pod_id: ""   # auto-generated if empty
```

## Persistence

**When Redis is enabled:**
- Channel topics are persisted to Redis immediately on `TOPIC` change.
- On server startup, channels and topics are hydrated from Redis.
- User account registrations (via `REGISTER`) are persisted permanently.
- Channel membership deltas are tracked in Redis for cross-pod awareness.

**When Redis is disabled:**
- All state (channels, topics, memberships) is in-memory only.
- A server restart loses all channel state. Clients must re-JOIN.
- Account commands (REGISTER, SETPASS, WHOACC) are unavailable.

## Supported IRC Commands

### Registration
| Command | Notes |
|---------|-------|
| `PASS <password>` | Optional; stores password for auto-login if account exists |
| `NICK <nickname>` | Set nick before or after registration; change after registration broadcasts to all shared channels |
| `USER <user> <mode> <unused> :<realname>` | Completes registration |
| `QUIT [:<reason>]` | Disconnects |

### Channel Operations
| Command | Notes |
|---------|-------|
| `JOIN <#channel>[,<#channel>]` | Join one or more channels |
| `PART <#channel>[,<#channel>] [:<reason>]` | Leave without deadlock |
| `TOPIC <#channel> [:<topic>]` | Query or set topic |
| `LIST [<#channel>]` | List channels |
| `NAMES [<#channel>]` | List members |
| `MODE <target> [<modes>]` | User and channel modes |

### Messaging
| Command | Notes |
|---------|-------|
| `PRIVMSG <target> :<text>` | Colons in body are preserved correctly |
| `NOTICE <target> :<text>` | Fails silently per RFC |

### Server Queries
| Command | Notes |
|---------|-------|
| `WHO [<mask>]` | |
| `WHOIS <nick>` | |
| `ISON <nick>...` | |
| `USERHOST <nick>...` | |
| `AWAY [:<message>]` | |
| `VERSION` | |
| `MOTD` | |
| `PING :<server>` | Server also PINGs clients every 90s |

### Operator Commands
| Command | Notes |
|---------|-------|
| `OPER <user> <pass>` | Authenticate from users.yaml |
| `KICK <#channel> <nick> [:<reason>]` | |
| `INVITE <nick> <#channel>` | |
| `KILL <nick> :<reason>` | |
| `WALLOPS :<message>` | |

## Known Limitations

- **TLS/SASL**: Not implemented. Use a TLS-terminating reverse proxy (e.g., nginx stream block) for encrypted connections.
- **Federation**: No multi-server federation. Distributed mode uses Redis pub/sub within one logical server cluster.
- **Operator password storage**: `users.yaml` stores plaintext passwords. Account passwords via REGISTER use bcrypt. TODO: unify.
- **Single lobby**: All clients auto-join `#lobby` on connection.
- **No services**: No NickServ/ChanServ services layer.
- **CHANREG persistence**: Channel registration metadata is stored in Redis but registration status is not restored on hydration. TODO.

## Testing

```bash
# Run all tests
go test ./...

# With race detection
go test -race ./...

# Specific test
go test ./pkg/server -run TestRegistrationWithoutPASS -v
```

Tests require no external services. Redis tests use [miniredis](https://github.com/alicebob/miniredis) — no real Redis needed.

## Building for Containers

The binary has no external dependencies at runtime:

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -ldflags="-w -s" -o girc ./cmd/girc

FROM scratch
COPY --from=builder /build/girc /girc
EXPOSE 6667
CMD ["/girc"]
```

The server uses environment variables for configuration when running from scratch (no `/etc/passwd`, no HOME dir).

## Rollback

If you need to roll back a deployment:

```bash
# Docker
docker stop girc && docker rm girc
docker run ... girc:previous-version

# Kubernetes
kubectl rollout undo deployment/girc -n girc
```

Redis data is backwards-compatible (additive changes only). Downgrading to a version before Redis support requires manually flushing `channel:*` and `user:*` keys if they cause errors.

## License

See LICENSE file for details.

## Credits

Created by [tehcyx](https://github.com/tehcyx)
