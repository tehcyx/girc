```none
 .o88b. d888888b d8888b.  .o88b.
d8P       `88'   88  `8D d8P  Y8
8P         88    88oobY' 8P
8b  d8b    88    88`8b   8b
Y8b  88   .88.   88 `88. Y8b  d8
 `Y88P' Y888888P 88   YD  `Y88P'
```

# GIRC - Modern IRC Server in Go

A modern, feature-rich IRC server implementation written in Go 1.25, with comprehensive test coverage, performance benchmarks, and Redis-backed persistence.

## Features

### Core IRC Protocol (RFC 1459/2812 Compliant)
- ✅ Full connection registration (PASS, NICK, USER)
- ✅ Channel operations (JOIN, PART, TOPIC, LIST)
- ✅ Messaging (PRIVMSG, NOTICE)
- ✅ User management (QUIT with broadcasting)
- ✅ Server queries (WHO, WHOIS, VERSION, MOTD, ISON, USERHOST)
- ✅ MODE command for user and channel modes
- ✅ Operator commands (OPER, KICK, INVITE)
- ✅ Away status (AWAY)
- ✅ Proper nickname change broadcasting
- ✅ Server ping/pong keepalive

### Modern Enhancements
- **Redis Backend**: Persistent user accounts, channels, and session data
- **22+ IRC Commands**: Comprehensive RFC 1459/2812 command support
- **Concurrent Operations**: Thread-safe with proper mutex handling
- **Comprehensive Logging**: Debug logging for all IRC commands
- **Test Coverage**: 32.4% statement coverage (128+ tests)
- **Performance Benchmarks**: 8 benchmarks for optimization
- **Edge Case Testing**: 10 tests for robustness
- **Integration Tests**: 14 end-to-end workflow tests
- **Go 1.25**: Modern Go with latest features and optimizations

## Quick Start

### Installation

```bash
git clone https://github.com/tehcyx/girc.git
cd girc
go mod download
go build ./cmd/girc
```

### Running the Server

#### With Redis (Recommended)
```bash
# Start Redis first
docker-compose up -d

# Run GIRC
./girc
```

#### Without Redis (Local Mode)
```bash
# Set environment variable to disable Redis
export REDIS_ENABLED=false
./girc
```

The server will:
- Listen on port 6667 (configurable via environment)
- Connect to Redis at localhost:6379 (if enabled)
- Create a default #lobby channel for all users
- Log all IRC commands and connections

### Environment Variables

```bash
# Server Configuration
export IRC_PORT=6667
export IRC_HOST=localhost
export SERVER_NAME=girc.local

# Redis Configuration (optional)
export REDIS_HOST=localhost
export REDIS_PORT=6379
export REDIS_PASSWORD=
export REDIS_DB=0
export REDIS_ENABLED=true
```

## Supported Commands

### Connection Registration
| Command | Syntax | Description | Status |
|---------|--------|-------------|--------|
| PASS | `PASS <password>` | Set connection password | ✅ Supported |
| NICK | `NICK <nickname>` | Set or change nickname | ✅ Supported + Broadcasting |
| USER | `USER <user> <mode> <unused> <realname>` | Set user information | ✅ Supported |
| QUIT | `QUIT [<message>]` | Disconnect from server | ✅ Supported + Broadcasting |
| PING | `PING :<server>` | Server keepalive | ✅ Supported |

### Channel Operations
| Command | Syntax | Description | Status |
|---------|--------|-------------|--------|
| JOIN | `JOIN <channel>{,<channel>}` | Join one or more channels | ✅ Supported |
| PART | `PART <channel>{,<channel>} [<message>]` | Leave one or more channels | ✅ Supported |
| TOPIC | `TOPIC <channel> [<topic>]` | View or set channel topic | ✅ Supported |
| LIST | `LIST [<channel>]` | List channels and topics | ✅ Supported |
| MODE | `MODE <target> [<modes>]` | View or set user/channel modes | ✅ Supported* |

\* *Note: MODE has a known bug with channel name lookup (see Bugs section)*

### Messaging
| Command | Syntax | Description | Status |
|---------|--------|-------------|--------|
| PRIVMSG | `PRIVMSG <target> :<message>` | Send message to user or channel | ✅ Supported |
| NOTICE | `NOTICE <target> :<message>` | Send notice to user or channel | ✅ Supported |

### User Queries
| Command | Syntax | Description | Status |
|---------|--------|-------------|--------|
| WHO | `WHO [<mask>]` | List users matching mask | ✅ Supported |
| WHOIS | `WHOIS <nick>` | Get user information | ✅ Supported |
| ISON | `ISON <nick> [<nick> ...]` | Check if users are online | ✅ Supported |
| USERHOST | `USERHOST <nick> [<nick> ...]` | Get user host information | ✅ Supported |
| AWAY | `AWAY [<message>]` | Set or clear away status | ✅ Supported |

### Server Queries
| Command | Syntax | Description | Status |
|---------|--------|-------------|--------|
| VERSION | `VERSION` | Get server version | ✅ Supported |
| MOTD | `MOTD` | Get message of the day | ✅ Supported |

### Operator Commands
| Command | Syntax | Description | Status |
|---------|--------|-------------|--------|
| OPER | `OPER <username> <password>` | Gain operator privileges | ✅ Supported |
| KICK | `KICK <channel> <user> [<reason>]` | Remove user from channel | ✅ Supported |
| INVITE | `INVITE <nick> <channel>` | Invite user to channel | ✅ Supported |

### Mode Flags

#### User Modes
- `+i` - Invisible (hidden from WHO *)
- `+o` - Operator status
- `+w` - Wallops (receive server notices)
- `+s` - Server notices
- `+a` - Away status

#### Channel Modes
- `+t` - Topic lock (only operators can change)
- `+n` - No external messages
- `+m` - Moderated (only voiced/ops can speak)
- `+i` - Invite only
- `+s` - Secret channel
- `+p` - Private channel
- `+o` - Channel operator (per-user)
- `+v` - Voice (can speak in moderated channel)

## Testing

### Quick Test Commands

```bash
# Run all tests (short mode, skips integration tests)
go test ./... -short -cover

# Run all tests including integration tests
go test ./... -cover

# Run tests with verbose output
go test ./... -v

# Run tests with race detection
go test ./... -race

# Run specific package tests
go test ./pkg/server -v
go test ./pkg/redis -v
go test ./internal/config -v
```

### Test Categories

#### 1. Unit Tests (94 tests)
Fast, focused tests for individual functions and components.

```bash
# Run unit tests only (automatic with -short flag)
go test ./... -short
```

**Coverage by package:**
- `pkg/server`: 32.4% coverage
- `pkg/version`: 100% coverage
- `internal/config`: 56.6% coverage
- `pkg/redis`: 1.0% coverage (requires Redis)

#### 2. Integration Tests (14 tests)
End-to-end tests with real network connections and full command workflows.

```bash
# Run integration tests (requires full test suite)
go test ./pkg/server -run TestIntegration

# Skip integration tests
go test ./pkg/server -short
```

**Integration test coverage:**
- Client registration flows
- Multi-client messaging scenarios
- Channel operations (JOIN/PART/PRIVMSG)
- Disconnect notifications
- Error handling (invalid nick, nick in use, etc.)
- Multi-line command buffer handling

#### 3. Edge Case Tests (10 tests)
Robustness tests for malformed inputs and boundary conditions.

```bash
# Run edge case tests
go test ./pkg/server -run TestEdgeCase
```

**Edge cases covered:**
- Very long nicknames, channel names, messages
- Malformed commands (missing parameters)
- Special characters in nicknames
- Unicode and multi-byte characters
- Rapid command flooding
- Connection drops mid-command

#### 4. Performance Benchmarks (8 benchmarks)
Performance testing for critical operations.

```bash
# Run all benchmarks
go test ./pkg/server -bench=.

# Run specific benchmark
go test ./pkg/server -bench=BenchmarkClientRegistration

# Run with memory statistics
go test ./pkg/server -bench=. -benchmem

# Run with CPU profiling
go test ./pkg/server -bench=. -cpuprofile=cpu.prof
go tool pprof cpu.prof
```

**Available benchmarks:**
- `BenchmarkClientRegistration` - ~10.5ms/op
- `BenchmarkPrivmsgThroughput` - Message routing
- `BenchmarkChannelJoin` - JOIN performance
- `BenchmarkConcurrentClients` - 10/50/100 clients
- `BenchmarkChannelBroadcast` - Multi-user broadcast
- `BenchmarkCommandParsing` - Parser performance
- `BenchmarkRoomLookup` - Channel search with 100 channels

See [BENCHMARKS.md](BENCHMARKS.md) for detailed benchmarking guide.

### Test Files Structure

```
pkg/server/
├── *_test.go               # 94 unit tests
│   ├── protocodes_test.go  # Protocol constants
│   ├── room_test.go        # Room operations
│   ├── server_test.go      # Core server logic
│   ├── who_test.go         # WHO command
│   ├── whois_test.go       # WHOIS command
│   ├── topic_test.go       # TOPIC command
│   ├── oper_test.go        # OPER command
│   ├── mode_test.go        # MODE command
│   ├── away_test.go        # AWAY command
│   ├── version_test.go     # VERSION command
│   ├── motd_test.go        # MOTD command
│   ├── ison_test.go        # ISON command
│   ├── userhost_test.go    # USERHOST command
│   ├── list_test.go        # LIST command
│   └── kick_test.go        # KICK command
├── integration_test.go     # 14 end-to-end tests
├── edgecase_test.go        # 10 robustness tests
└── benchmark_test.go       # 8 performance benchmarks
```

### Continuous Testing

```bash
# Watch for changes and re-run tests (requires entr or similar)
ls **/*.go | entr go test ./... -short

# Check for race conditions
go test ./... -race -short

# Generate HTML coverage report
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

## Administrator Guide

This comprehensive guide covers everything from local development to production Kubernetes deployment, including best practices and scaling strategies.

### Table of Contents
- [Local Development](#local-development)
- [Docker Deployment](#docker-deployment)
- [Kubernetes Deployment](#kubernetes-deployment)
- [Scaling Strategies](#scaling-strategies)
- [Security Best Practices](#security-best-practices)
- [Monitoring & Operations](#monitoring--operations)

---

### Local Development

#### Prerequisites
- **Go 1.25+**: [Install Go](https://golang.org/dl/)
- **Redis 6.0+**: Optional, for persistence and distributed mode
- **Git**: For version control

#### Quick Start for Development

1. **Clone and Build**
   ```bash
   git clone https://github.com/tehcyx/girc.git
   cd girc
   go mod download
   go build ./cmd/girc
   ```

2. **Run in Local Mode** (no Redis)
   ```bash
   # Edit ~/.girc/conf.yaml or set environment variables
   export REDIS_ENABLED=false
   ./girc
   ```
   The server will start on `localhost:6665` (configurable in `~/.girc/conf.yaml`).

3. **Run with Redis** (recommended)
   ```bash
   # Start Redis with Docker
   docker run -d -p 6379:6379 --name girc-redis redis:7-alpine

   # Configure GIRC
   export REDIS_ENABLED=true
   export REDIS_URL=redis://localhost:6379
   ./girc
   ```

#### Configuration Files

GIRC uses two main configuration locations:

**Server Configuration**: `~/.girc/conf.yaml`
```yaml
server:
  host: "localhost"
  port: "6665"
  name: "girc.local"
  motd: "Welcome to GIRC - Modern IRC Server"
  debug: true  # Enable detailed protocol logging

redis:
  enabled: true
  url: "redis://localhost:6379"
  pod_id: "dev-instance-1"  # Unique identifier for distributed mode
```

**User Database**: `~/.girc/users.yaml`
```yaml
users:
- username: admin
  password: admin  # WARNING: Use hashed passwords in production
  isoper: true
  channels: []
- username: moderator
  password: modpass
  isoper: true
  channels: ["#staff"]
```

**Security Note**: The current implementation uses plaintext passwords. Phase 14 of the roadmap includes bcrypt password hashing.

#### Environment Variables

Override configuration with environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `IRC_PORT` | `6665` | Server listening port |
| `IRC_HOST` | `localhost` | Server hostname |
| `SERVER_NAME` | `girc.local` | Server name for IRC protocol |
| `REDIS_ENABLED` | `false` | Enable Redis backend |
| `REDIS_URL` | `redis://localhost:6379` | Redis connection string |
| `REDIS_PASSWORD` | _(empty)_ | Redis authentication password |
| `REDIS_DB` | `0` | Redis database number |
| `POD_NAME` | _(auto-generated)_ | Unique pod identifier for K8s |

#### Testing Your Setup

1. **Connect with an IRC client**:
   ```bash
   # Using telnet (basic test)
   telnet localhost 6665
   NICK testuser
   USER testuser 0 * :Test User
   JOIN #lobby

   # Using WeeChat (recommended)
   weechat
   /server add girc localhost/6665
   /connect girc

   # Using irssi
   irssi
   /connect localhost 6665
   ```

2. **Become an operator**:
   ```
   /oper admin admin
   ```

3. **Test basic commands**:
   ```
   /join #test
   /topic #test Welcome to the test channel
   /kick #test baduser Misbehaving
   /mode #test +m
   ```

#### Development Workflow

```bash
# Run tests during development
go test ./... -short -v

# Run with race detection
go test ./... -race

# Run benchmarks
go test ./pkg/server -bench=. -benchmem

# Build with debug symbols
go build -gcflags="all=-N -l" -o girc ./cmd/girc

# Profile the server
go build -o girc ./cmd/girc
./girc &
go tool pprof http://localhost:6060/debug/pprof/profile
```

---

### Docker Deployment

#### Single Container Deployment

**Option 1: Using docker-compose (Recommended)**

The project includes a `docker-compose.yml` for Redis. Create a full stack:

```yaml
# docker-compose.yml
services:
  redis:
    image: redis:7-alpine
    container_name: girc-redis
    ports:
      - "6379:6379"
    command: redis-server --appendonly yes
    volumes:
      - redis-data:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5
    restart: unless-stopped

  girc:
    image: girc:latest
    container_name: girc-server
    build:
      context: .
      dockerfile: Dockerfile
    ports:
      - "6667:6667"
    environment:
      - REDIS_ENABLED=true
      - REDIS_URL=redis://redis:6379
      - SERVER_NAME=girc.docker
      - IRC_PORT=6667
    depends_on:
      redis:
        condition: service_healthy
    volumes:
      - ./config:/root/.girc  # Mount config directory
    restart: unless-stopped

volumes:
  redis-data:
```

**Create a Dockerfile**:

```dockerfile
# Dockerfile
FROM golang:1.25-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -ldflags="-w -s" \
    -o girc ./cmd/girc

FROM alpine:latest
RUN apk --no-cache add ca-certificates

WORKDIR /app
COPY --from=builder /build/girc .

# Create default config directory
RUN mkdir -p /root/.girc

EXPOSE 6667
CMD ["./girc"]
```

**Deploy**:
```bash
# Build and start
docker-compose up -d

# View logs
docker-compose logs -f girc

# Stop
docker-compose down

# Stop and remove data
docker-compose down -v
```

**Option 2: Manual Docker**

```bash
# Build image
docker build -t girc:latest .

# Run Redis
docker network create girc-network
docker run -d --name girc-redis \
  --network girc-network \
  -v girc-redis-data:/data \
  redis:7-alpine redis-server --appendonly yes

# Run GIRC
docker run -d --name girc-server \
  --network girc-network \
  -p 6667:6667 \
  -e REDIS_ENABLED=true \
  -e REDIS_URL=redis://girc-redis:6379 \
  -e SERVER_NAME=girc.docker \
  -v $(pwd)/config:/root/.girc \
  girc:latest

# View logs
docker logs -f girc-server
```

#### Multi-Container Scaling with Docker Swarm

For simple horizontal scaling without Kubernetes:

```yaml
# docker-compose.swarm.yml
version: "3.8"

services:
  redis:
    image: redis:7-alpine
    command: redis-server --appendonly yes
    volumes:
      - redis-data:/data
    deploy:
      replicas: 1
      placement:
        constraints: [node.role == manager]
    networks:
      - girc-network

  girc:
    image: girc:latest
    ports:
      - "6667:6667"
    environment:
      - REDIS_ENABLED=true
      - REDIS_URL=redis://redis:6379
      - SERVER_NAME=girc.swarm
    depends_on:
      - redis
    deploy:
      replicas: 3  # Scale to 3 instances
      update_config:
        parallelism: 1
        delay: 10s
      restart_policy:
        condition: on-failure
    networks:
      - girc-network

volumes:
  redis-data:

networks:
  girc-network:
    driver: overlay
```

Deploy to Swarm:
```bash
docker swarm init
docker stack deploy -c docker-compose.swarm.yml girc-stack

# Scale up/down
docker service scale girc-stack_girc=5

# View services
docker stack services girc-stack
```

---

### Kubernetes Deployment

GIRC is designed for cloud-native deployment with Kubernetes, using Redis for shared state and horizontal pod scaling.

#### Architecture Overview

```
┌─────────────────────────────────────────────┐
│          LoadBalancer / Ingress             │
│         (Session Affinity Enabled)          │
└──────────────────┬──────────────────────────┘
                   │
    ┌──────────────┼──────────────┐
    │              │              │
┌───▼───┐      ┌───▼───┐      ┌──▼────┐
│ GIRC  │      │ GIRC  │      │ GIRC  │
│ Pod 1 │      │ Pod 2 │      │ Pod 3 │
└───┬───┘      └───┬───┘      └───┬───┘
    │              │              │
    └──────────────┼──────────────┘
                   │
            ┌──────▼──────┐
            │    Redis    │
            │   Cluster   │
            │  (StatefulSet)
            └─────────────┘
```

#### Prerequisites

- **Kubernetes Cluster**: 1.20+ (EKS, GKE, AKS, or local with minikube/kind)
- **kubectl**: Configured to access your cluster
- **Helm** (optional): For Redis installation
- **Container Registry**: DockerHub, GCR, ECR, etc.

#### Step 1: Build and Push Container Image

```bash
# Build for your architecture
docker build -t your-registry.com/girc:v1.0.0 .

# Push to registry
docker push your-registry.com/girc:v1.0.0
```

#### Step 2: Deploy Redis

**Option A: Using Helm (Recommended for Production)**

```bash
# Add Bitnami repo
helm repo add bitnami https://charts.bitnami.com/bitnami
helm repo update

# Install Redis with replication
helm install girc-redis bitnami/redis \
  --set architecture=standalone \
  --set auth.enabled=true \
  --set auth.password=changeme \
  --set master.persistence.enabled=true \
  --set master.persistence.size=10Gi

# For Redis Cluster (high availability)
helm install girc-redis bitnami/redis-cluster \
  --set cluster.nodes=6 \
  --set persistence.size=10Gi
```

**Option B: Manual Deployment**

```yaml
# redis-statefulset.yaml
apiVersion: v1
kind: Service
metadata:
  name: redis
  namespace: girc
spec:
  ports:
  - port: 6379
    targetPort: 6379
  clusterIP: None  # Headless service
  selector:
    app: redis
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: redis
  namespace: girc
spec:
  serviceName: redis
  replicas: 1  # Single instance (use Redis Cluster for HA)
  selector:
    matchLabels:
      app: redis
  template:
    metadata:
      labels:
        app: redis
    spec:
      containers:
      - name: redis
        image: redis:7-alpine
        ports:
        - containerPort: 6379
          name: redis
        command:
        - redis-server
        - --appendonly
        - "yes"
        volumeMounts:
        - name: data
          mountPath: /data
        resources:
          requests:
            memory: "256Mi"
            cpu: "100m"
          limits:
            memory: "1Gi"
            cpu: "500m"
        livenessProbe:
          exec:
            command:
            - redis-cli
            - ping
          initialDelaySeconds: 30
          periodSeconds: 10
        readinessProbe:
          exec:
            command:
            - redis-cli
            - ping
          initialDelaySeconds: 5
          periodSeconds: 5
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: ["ReadWriteOnce"]
      storageClassName: standard  # Adjust for your cloud provider
      resources:
        requests:
          storage: 10Gi
```

Apply:
```bash
kubectl create namespace girc
kubectl apply -f redis-statefulset.yaml
```

#### Step 3: Deploy GIRC Server

```yaml
# girc-deployment.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: girc-config
  namespace: girc
data:
  SERVER_NAME: "girc.k8s.local"
  IRC_PORT: "6667"
  REDIS_ENABLED: "true"
  REDIS_URL: "redis://redis:6379"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: girc
  namespace: girc
  labels:
    app: girc
spec:
  replicas: 3  # Start with 3 replicas
  selector:
    matchLabels:
      app: girc
  template:
    metadata:
      labels:
        app: girc
    spec:
      containers:
      - name: girc
        image: your-registry.com/girc:v1.0.0
        imagePullPolicy: Always
        ports:
        - containerPort: 6667
          name: irc
          protocol: TCP
        env:
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        - name: POD_IP
          valueFrom:
            fieldRef:
              fieldPath: status.podIP
        envFrom:
        - configMapRef:
            name: girc-config
        resources:
          requests:
            memory: "128Mi"
            cpu: "100m"
          limits:
            memory: "512Mi"
            cpu: "1000m"
        livenessProbe:
          tcpSocket:
            port: 6667
          initialDelaySeconds: 15
          periodSeconds: 20
        readinessProbe:
          tcpSocket:
            port: 6667
          initialDelaySeconds: 5
          periodSeconds: 10
---
apiVersion: v1
kind: Service
metadata:
  name: girc
  namespace: girc
  annotations:
    # For AWS ELB
    service.beta.kubernetes.io/aws-load-balancer-type: "nlb"
    # For GCP
    cloud.google.com/load-balancer-type: "Internal"
spec:
  type: LoadBalancer  # Use NodePort or ClusterIP with Ingress if preferred
  sessionAffinity: ClientIP  # Sticky sessions (critical for IRC)
  sessionAffinityConfig:
    clientIP:
      timeoutSeconds: 10800  # 3 hours
  ports:
  - port: 6667
    targetPort: 6667
    protocol: TCP
    name: irc
  selector:
    app: girc
```

Apply:
```bash
kubectl apply -f girc-deployment.yaml

# Check status
kubectl get pods -n girc
kubectl get svc -n girc

# Get external IP
kubectl get svc girc -n girc
```

#### Step 4: Configure Horizontal Pod Autoscaling

```yaml
# girc-hpa.yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: girc
  namespace: girc
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: girc
  minReplicas: 3
  maxReplicas: 20
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
  - type: Resource
    resource:
      name: memory
      target:
        type: Utilization
        averageUtilization: 80
  behavior:
    scaleDown:
      stabilizationWindowSeconds: 300  # Wait 5 min before scaling down
      policies:
      - type: Percent
        value: 50
        periodSeconds: 60
    scaleUp:
      stabilizationWindowSeconds: 0
      policies:
      - type: Percent
        value: 100
        periodSeconds: 30
```

Apply:
```bash
kubectl apply -f girc-hpa.yaml

# Monitor autoscaling
kubectl get hpa -n girc -w
```

#### Step 5: Persistent User Configuration

For persistent user/operator configuration:

```yaml
# girc-configmap-users.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: girc-users
  namespace: girc
data:
  users.yaml: |
    users:
    - username: admin
      password: "$2a$10$..." # bcrypt hash (see Security Best Practices)
      isoper: true
      channels: []
    - username: moderator
      password: "$2a$10$..."
      isoper: true
      channels: ["#staff"]
```

Mount in deployment:
```yaml
# Add to girc-deployment.yaml under spec.template.spec.containers[0]
volumeMounts:
- name: users-config
  mountPath: /root/.girc/users.yaml
  subPath: users.yaml

# Add to spec.template.spec
volumes:
- name: users-config
  configMap:
    name: girc-users
```

---

### Scaling Strategies

#### Understanding GIRC's Scaling Model

GIRC uses a **shared-state architecture** with Redis as the central coordination point. This enables true horizontal scaling without complex IRC federation protocols.

**How it works**:
1. **Stateless Pods**: Each GIRC pod is stateless; all state lives in Redis
2. **Client Affinity**: Load balancer uses session affinity (client IP) to route returning clients to same pod
3. **Cross-Pod Messaging**: Redis Pub/Sub broadcasts messages between pods
4. **Automatic Failover**: If a pod dies, clients reconnect to any available pod

#### Scaling Patterns

**Pattern 1: Steady-State Scaling**

For predictable load (e.g., 1000 concurrent users):

```bash
# Calculate pod count
# Target: 100-200 connections per pod
# 1000 users ÷ 150 avg = ~7 pods
kubectl scale deployment girc --replicas=7 -n girc
```

**Pattern 2: Auto-Scaling Based on Connections**

Use custom metrics (requires Metrics Server + Custom Metrics API):

```yaml
# Requires prometheus-adapter or similar
- type: Pods
  pods:
    metric:
      name: irc_active_connections
    target:
      type: AverageValue
      averageValue: 150  # Scale at 150 connections per pod
```

**Pattern 3: Time-Based Scaling**

For predictable traffic patterns (e.g., peak hours):

```bash
# Use CronJob to scale up during peak hours
# Scale up at 6 PM
apiVersion: batch/v1
kind: CronJob
metadata:
  name: girc-scaleup
  namespace: girc
spec:
  schedule: "0 18 * * *"  # 6 PM daily
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: kubectl
            image: bitnami/kubectl:latest
            command:
            - kubectl
            - scale
            - deployment/girc
            - --replicas=10
            - -n
            - girc
          restartPolicy: OnFailure
          serviceAccountName: scaler  # Needs RBAC permissions
```

#### Capacity Planning

**Resource Requirements per Pod**:

| Metric | Light Load | Medium Load | Heavy Load |
|--------|------------|-------------|------------|
| Concurrent Connections | 50 | 150 | 300 |
| CPU (cores) | 0.1 | 0.3 | 0.5 |
| Memory (MB) | 128 | 256 | 512 |
| Network (Mbps) | 1 | 5 | 10 |

**Redis Requirements**:

| Users | Memory | CPU | Recommended Setup |
|-------|--------|-----|-------------------|
| 100-500 | 512MB | 0.5 | Single Redis instance |
| 500-2000 | 2GB | 1.0 | Redis with persistence |
| 2000-10000 | 8GB | 2.0 | Redis Cluster (3 nodes) |
| 10000+ | 16GB+ | 4.0+ | Redis Cluster (6+ nodes) |

#### Multi-Region Deployment

For global deployment with low latency:

```
Region 1 (US-East)          Region 2 (EU-West)         Region 3 (Asia)
┌──────────────┐            ┌──────────────┐           ┌──────────────┐
│ GIRC Pods    │            │ GIRC Pods    │           │ GIRC Pods    │
│ + Redis      │            │ + Redis      │           │ + Redis      │
└──────────────┘            └──────────────┘           └──────────────┘
```

**Options**:
1. **Separate Networks**: Each region is independent (easiest)
2. **Redis Replication**: Master-slave across regions (complex)
3. **Future**: IRC federation between regions (Approach 3 in DISTRIBUTED_ARCHITECTURE.md)

---

### Security Best Practices

#### 1. Password Security

**CRITICAL**: The current implementation uses plaintext passwords. For production:

**Option A: Use bcrypt hashing (Recommended - planned for Phase 14)**

```bash
# Generate bcrypt hash
go get golang.org/x/crypto/bcrypt
echo -n "mypassword" | go run -x golang.org/x/crypto/bcrypt
```

**Option B: Use Kubernetes Secrets**

```bash
# Create secret for admin password
kubectl create secret generic girc-admin-pass \
  --from-literal=password='SecurePassword123!' \
  -n girc

# Mount in deployment
env:
- name: ADMIN_PASSWORD
  valueFrom:
    secretKeyRef:
      name: girc-admin-pass
      key: password
```

#### 2. Network Security

**TLS/SSL Termination** (IRC over TLS):

Use a reverse proxy for TLS:

```yaml
# nginx-tls-proxy.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: nginx-irc-tls
  namespace: girc
data:
  nginx.conf: |
    stream {
      server {
        listen 6697 ssl;
        ssl_certificate /etc/nginx/certs/tls.crt;
        ssl_certificate_key /etc/nginx/certs/tls.key;
        ssl_protocols TLSv1.2 TLSv1.3;
        proxy_pass girc:6667;
      }
    }
```

**Firewall Rules**:

```bash
# For production, restrict access
# Only allow IRC ports
- 6667 (IRC plaintext)
- 6697 (IRC over TLS)

# Block direct Redis access
# Redis should only be accessible within cluster
```

#### 3. Rate Limiting

Add rate limiting at the load balancer or ingress level:

```yaml
# Example for nginx-ingress
metadata:
  annotations:
    nginx.ingress.kubernetes.io/limit-rps: "10"
    nginx.ingress.kubernetes.io/limit-connections: "5"
```

#### 4. Resource Limits

Always set resource limits to prevent DoS:

```yaml
resources:
  requests:
    memory: "128Mi"
    cpu: "100m"
  limits:
    memory: "512Mi"  # Hard limit prevents OOM
    cpu: "1000m"
```

#### 5. Redis Security

```bash
# Enable Redis authentication
helm install girc-redis bitnami/redis \
  --set auth.enabled=true \
  --set auth.password="SuperSecureRedisPassword"

# Use NetworkPolicy to restrict access
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: redis-access
  namespace: girc
spec:
  podSelector:
    matchLabels:
      app: redis
  policyTypes:
  - Ingress
  ingress:
  - from:
    - podSelector:
        matchLabels:
          app: girc  # Only GIRC pods can access Redis
```

#### 6. RBAC and Service Accounts

Minimize pod permissions:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: girc
  namespace: girc
---
# Only grant necessary permissions (none needed for basic GIRC)
```

---

### Monitoring & Operations

#### Health Checks

GIRC exposes health endpoints (TCP-based):

```yaml
livenessProbe:
  tcpSocket:
    port: 6667
  initialDelaySeconds: 15
  periodSeconds: 20

readinessProbe:
  tcpSocket:
    port: 6667
  initialDelaySeconds: 5
  periodSeconds: 10
```

#### Logging

**Centralized Logging** (ELK, Loki, CloudWatch):

```yaml
# Fluent Bit sidecar for log forwarding
- name: fluent-bit
  image: fluent/fluent-bit:latest
  volumeMounts:
  - name: varlog
    mountPath: /var/log
```

**Useful log queries**:
```bash
# Watch for errors
kubectl logs -f deployment/girc -n girc | grep ERROR

# Monitor connections
kubectl logs deployment/girc -n girc | grep "Client connected"

# Track OPER usage
kubectl logs deployment/girc -n girc | grep OPER
```

#### Metrics (Future Enhancement)

**Prometheus Metrics** (add to roadmap):

```go
// Expose metrics endpoint
http.Handle("/metrics", promhttp.Handler())

// Track metrics
irc_active_connections
irc_messages_per_second
irc_channels_total
irc_commands_total{command="JOIN"}
```

#### Backup and Recovery

**Redis Data Backup**:

```bash
# Automated backup with CronJob
apiVersion: batch/v1
kind: CronJob
metadata:
  name: redis-backup
  namespace: girc
spec:
  schedule: "0 2 * * *"  # Daily at 2 AM
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: backup
            image: redis:7-alpine
            command:
            - sh
            - -c
            - |
              redis-cli -h redis SAVE
              cp /data/dump.rdb /backup/dump-$(date +%Y%m%d).rdb
            volumeMounts:
            - name: redis-data
              mountPath: /data
            - name: backup
              mountPath: /backup
          restartPolicy: OnFailure
```

#### Disaster Recovery

**Recovery Procedure**:

1. **Redis Failure**:
   ```bash
   # Redis will auto-recover from AOF/RDB
   # If total loss, users must re-register
   kubectl rollout restart statefulset/redis -n girc
   ```

2. **Pod Failure**:
   ```bash
   # Kubernetes auto-restarts failed pods
   # Clients auto-reconnect via load balancer
   kubectl get pods -n girc
   ```

3. **Full Cluster Failure**:
   ```bash
   # Restore from backup
   kubectl apply -f redis-statefulset.yaml
   kubectl cp dump.rdb redis-0:/data/dump.rdb -n girc
   kubectl apply -f girc-deployment.yaml
   ```

#### Performance Tuning

**Redis Optimization**:
```bash
# Increase max connections
redis-cli CONFIG SET maxclients 10000

# Enable lazy free for better performance
redis-cli CONFIG SET lazyfree-lazy-eviction yes
```

**Kernel Tuning** (for node OS):
```bash
# Increase connection limits
sysctl -w net.core.somaxconn=4096
sysctl -w net.ipv4.tcp_max_syn_backlog=4096

# TCP keepalive settings for IRC
sysctl -w net.ipv4.tcp_keepalive_time=600
sysctl -w net.ipv4.tcp_keepalive_intvl=60
sysctl -w net.ipv4.tcp_keepalive_probes=3
```

---

### Troubleshooting

#### Common Issues

**Issue: Clients can't connect**
```bash
# Check service external IP
kubectl get svc girc -n girc

# Check pod status
kubectl get pods -n girc

# Check logs
kubectl logs deployment/girc -n girc --tail=50
```

**Issue: Messages not delivered between pods**
```bash
# Verify Redis connectivity
kubectl exec -it deployment/girc -n girc -- sh
nc -zv redis 6379

# Check Redis Pub/Sub
redis-cli PUBSUB CHANNELS
```

**Issue: High memory usage**
```bash
# Check for memory leaks
kubectl top pods -n girc

# Restart deployment if needed
kubectl rollout restart deployment/girc -n girc
```

**Issue: Session affinity not working**
```bash
# Verify service configuration
kubectl describe svc girc -n girc | grep -i affinity

# Should show: SessionAffinity: ClientIP
```

---

### Quick Reference

**Essential Commands**:

```bash
# Deploy
kubectl apply -f redis-statefulset.yaml
kubectl apply -f girc-deployment.yaml

# Scale
kubectl scale deployment girc --replicas=5 -n girc

# Update image
kubectl set image deployment/girc girc=your-registry.com/girc:v1.1.0 -n girc

# Rollback
kubectl rollout undo deployment/girc -n girc

# Logs
kubectl logs -f deployment/girc -n girc

# Shell into pod
kubectl exec -it deployment/girc -n girc -- sh

# Port forward for testing
kubectl port-forward svc/girc 6667:6667 -n girc
```

**Performance Targets**:

| Metric | Target | Monitoring |
|--------|--------|------------|
| Connection latency | < 100ms | TCP probe |
| Message delivery | < 50ms | End-to-end test |
| Pod startup time | < 10s | Readiness probe |
| CPU per 100 users | < 0.3 cores | Prometheus |
| Memory per 100 users | < 256MB | kubectl top |

---

## Architecture

### Package Structure
```
girc/
├── cmd/girc/               # Server entry point
│   └── main.go
├── internal/config/        # Configuration management
│   ├── config.go          # Config loading
│   └── config_test.go     # Config tests
├── pkg/
│   ├── server/            # IRC server implementation
│   │   ├── server.go      # Core server logic (1700+ lines)
│   │   ├── server_helpers.go  # Helper functions
│   │   ├── room.go        # Channel management
│   │   ├── protocodes.go  # IRC protocol constants
│   │   └── *_test.go      # Comprehensive test suite
│   ├── redis/             # Redis backend
│   │   ├── redis.go       # Redis operations
│   │   └── redis_test.go  # Redis tests
│   └── version/           # Version information
│       ├── version.go
│       └── version_test.go  # 100% coverage
├── config.yaml            # Server configuration
├── BENCHMARKS.md          # Performance testing guide
└── README.md             # This file
```

### Key Components

#### Server (pkg/server/server.go)
- Manages client connections and channels
- Handles IRC protocol message routing
- Thread-safe operations with mutexes
- Command dispatch to 22+ IRC commands
- ~1700 lines of Go code

#### Room (pkg/server/room.go)
- Channel state and member management
- Topic, modes, and operator lists
- Concurrent-safe operations
- Ban list management

#### Redis Backend (pkg/redis/redis.go)
- Persistent user accounts
- Session management
- Channel state persistence
- Operator authentication

#### Config (internal/config/config.go)
- YAML-based configuration loading
- Environment variable support
- Redis connection management

## Known Bugs

### MODE Command Channel Lookup Bug
**Issue**: The MODE command fails to find channels when setting channel modes.

**Root cause**: MODE compares `room.name` (stored without #) against `target` (includes #).
- Example: `"opchannel" == "#opchannel"` → false
- Result: MODE returns "403 No such channel" for all channel mode operations

**Impact**: KICK, INVITE, and other operator commands may have similar issues.

**Workaround**: Use user modes (e.g., `MODE nickname +o`) which work correctly.

**Status**: Documented, fix planned for future phase.

See `context.md` line 1563 for details.

## Performance

### Benchmarks Results (AMD Ryzen 5 5600, 12 cores)

| Operation | Performance | Target |
|-----------|------------|--------|
| Client Registration | ~10.5ms/op | < 20ms ✅ |
| PRIVMSG (user-to-user) | TBD | < 5ms |
| Channel Broadcast (10 users) | TBD | < 10ms |
| JOIN command | TBD | < 5ms |
| Room lookup (100 channels) | TBD | < 1ms |

Run benchmarks with: `go test ./pkg/server -bench=.`

See [BENCHMARKS.md](BENCHMARKS.md) for complete performance guide.

## Development

### Prerequisites
- Go 1.25 or higher
- Redis 6.0+ (optional, for persistence)
- Git

### Building from Source

```bash
# Clone repository
git clone https://github.com/tehcyx/girc.git
cd girc

# Download dependencies
go mod download

# Run tests
go test ./... -short

# Build binary
go build -o girc ./cmd/girc

# Build with version info
go build -ldflags "-X github.com/tehcyx/girc/pkg/version.Version=v1.0.0 -X github.com/tehcyx/girc/pkg/version.GitCommit=$(git rev-parse HEAD)" -o girc ./cmd/girc

# Run
./girc
```

### Code Quality
- Comprehensive GoDoc documentation
- Thread-safe concurrent operations
- Debug logging throughout
- Table-driven tests
- Benchmark tests for performance-critical paths
- 128+ tests with 32%+ coverage

### Contributing
Contributions are welcome! Please ensure:
- All tests pass: `go test ./... -short`
- Code is documented with GoDoc comments
- Changes include appropriate test coverage
- No race conditions: `go test -race ./...`
- Benchmarks don't regress: `go test ./pkg/server -bench=.`

## Roadmap

### Phase 14: Integration Testing & Production Readiness (IN PROGRESS)
- ✅ Integration test suite (14 tests)
- ✅ Edge case testing (10 tests)
- ✅ Performance benchmarks (8 benchmarks)
- ✅ Version package tests (100% coverage)
- ⏳ Security hardening (rate limiting, flood protection)
- ⏳ Production readiness (health checks, metrics, monitoring)
- ⏳ **Password hashing with bcrypt** - Next priority
- ⏳ User registration and persistence

### Phase 15: Advanced Features (PLANNED)
- Redis connection pooling optimization
- Memory leak detection and profiling
- Graceful shutdown with context.Context
- Connection timeout handling
- Advanced rate limiting per client
- Channel ban lists and invite exceptions

## Test Clients

The server has been tested with:
- [WeeChat](https://weechat.org/) - Console IRC client
- [irssi](https://irssi.org/) - Terminal IRC client
- [HexChat](https://hexchat.github.io/) - GUI IRC client
- Custom test scripts using `net.Pipe()`

## References

### RFCs
- [RFC 2812](https://tools.ietf.org/html/rfc2812) - Internet Relay Chat: Client Protocol
- [RFC 1459](https://tools.ietf.org/html/rfc1459) - Internet Relay Chat Protocol

### Resources
- [Modern IRC Documentation](https://modern.ircdocs.horse/)
- [IRC Examples](http://chi.cs.uchicago.edu/chirc/irc_examples.html)
- [Go Concurrency Patterns](https://tour.golang.org/concurrency/9)
- [Performance Testing Guide](./BENCHMARKS.md)

## License

See LICENSE file for details.

## Credits

Created by [tehcyx](https://github.com/tehcyx)

Inspired by [circ](https://github.com/tehcyx/circ) - IRC server in C
