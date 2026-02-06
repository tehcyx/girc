// Package redis provides distributed state management using Redis.
// This enables horizontal scaling across multiple server pods in Kubernetes.
package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client wraps the Redis client and provides IRC state management
type Client struct {
	rdb    *redis.Client
	pubsub *redis.PubSub
	podID  string // Unique identifier for this server pod
}

// ClientData represents a connected IRC client's state
type ClientData struct {
	UUID     string   `json:"uuid"`
	Nick     string   `json:"nick"`
	User     string   `json:"user"`
	Host     string   `json:"host"`
	PodID    string   `json:"pod_id"`
	Channels []string `json:"channels"`
}

// ChannelData represents an IRC channel's state
type ChannelData struct {
	Name         string            `json:"name"`
	Topic        string            `json:"topic"`
	Members      map[string]bool   `json:"members"`   // UUID -> present
	Operators    map[string]bool   `json:"operators"` // UUID -> is_op
	Modes        string            `json:"modes"`
	Registered   bool              `json:"registered"`    // Whether channel is registered
	Founder      string            `json:"founder"`       // Account name of founder
	RegisteredAt int64             `json:"registered_at"` // Unix timestamp
	Description  string            `json:"description"`   // Channel description
}

// Event represents a cross-pod IRC event
type Event struct {
	Type string                 `json:"type"` // JOIN, PART, PRIVMSG, NICK, QUIT, KICK, POD_HEARTBEAT, POD_SHUTDOWN
	Data map[string]interface{} `json:"data"`
}

// PodInfo represents metadata about a running pod
type PodInfo struct {
	PodID        string    `json:"pod_id"`
	StartTime    time.Time `json:"start_time"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	ClientCount  int       `json:"client_count"`
	Version      string    `json:"version"`
}

// NewClient creates a new Redis client for distributed state management
func NewClient(redisURL string, podID string) (*Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URL: %w", err)
	}

	rdb := redis.NewClient(opts)

	// Test connection with shorter timeout for faster failures
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &Client{
		rdb:   rdb,
		podID: podID,
	}, nil
}

// Close closes the Redis connection
func (c *Client) Close() error {
	if c.pubsub != nil {
		if err := c.pubsub.Close(); err != nil {
			return fmt.Errorf("failed to close pubsub: %w", err)
		}
	}
	return c.rdb.Close()
}

// RegisterClient registers a client in Redis
func (c *Client) RegisterClient(ctx context.Context, client ClientData) error {
	client.PodID = c.podID

	// Store client data
	data, err := json.Marshal(client)
	if err != nil {
		return fmt.Errorf("failed to marshal client data: %w", err)
	}

	pipe := c.rdb.Pipeline()

	// Store client by UUID
	pipe.Set(ctx, fmt.Sprintf("client:%s", client.UUID), data, 0)

	// Store nick -> UUID mapping for uniqueness checks
	pipe.Set(ctx, fmt.Sprintf("nick:%s", client.Nick), client.UUID, 0)

	// Add to pod's client set
	pipe.SAdd(ctx, fmt.Sprintf("pod:%s:clients", c.podID), client.UUID)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to register client: %w", err)
	}

	return nil
}

// GetClient retrieves a client by UUID
func (c *Client) GetClient(ctx context.Context, uuid string) (*ClientData, error) {
	data, err := c.rdb.Get(ctx, fmt.Sprintf("client:%s", uuid)).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("client not found: %s", uuid)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get client: %w", err)
	}

	var client ClientData
	if err := json.Unmarshal([]byte(data), &client); err != nil {
		return nil, fmt.Errorf("failed to unmarshal client data: %w", err)
	}

	return &client, nil
}

// GetClientByNick retrieves a client by nickname
func (c *Client) GetClientByNick(ctx context.Context, nick string) (*ClientData, error) {
	// First get UUID from nick mapping
	uuid, err := c.rdb.Get(ctx, fmt.Sprintf("nick:%s", nick)).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("nick not found: %s", nick)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get nick mapping: %w", err)
	}

	return c.GetClient(ctx, uuid)
}

// UnregisterClient removes a client from Redis
func (c *Client) UnregisterClient(ctx context.Context, uuid string) error {
	// Get client data first to remove nick mapping
	client, err := c.GetClient(ctx, uuid)
	if err != nil {
		return err
	}

	pipe := c.rdb.Pipeline()
	pipe.Del(ctx, fmt.Sprintf("client:%s", uuid))
	pipe.Del(ctx, fmt.Sprintf("nick:%s", client.Nick))
	pipe.SRem(ctx, fmt.Sprintf("pod:%s:clients", c.podID), uuid)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to unregister client: %w", err)
	}

	return nil
}

// UpdateClientNick updates a client's nickname
func (c *Client) UpdateClientNick(ctx context.Context, uuid, oldNick, newNick string) error {
	// Check if new nick is available
	exists, err := c.rdb.Exists(ctx, fmt.Sprintf("nick:%s", newNick)).Result()
	if err != nil {
		return fmt.Errorf("failed to check nick availability: %w", err)
	}
	if exists > 0 {
		return fmt.Errorf("nick already in use: %s", newNick)
	}

	// Get client data
	client, err := c.GetClient(ctx, uuid)
	if err != nil {
		return err
	}

	// Update nick
	client.Nick = newNick

	data, err := json.Marshal(client)
	if err != nil {
		return fmt.Errorf("failed to marshal client data: %w", err)
	}

	pipe := c.rdb.Pipeline()
	pipe.Set(ctx, fmt.Sprintf("client:%s", uuid), data, 0)
	pipe.Del(ctx, fmt.Sprintf("nick:%s", oldNick))
	pipe.Set(ctx, fmt.Sprintf("nick:%s", newNick), uuid, 0)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to update nick: %w", err)
	}

	return nil
}

// JoinChannel adds a client to a channel
func (c *Client) JoinChannel(ctx context.Context, uuid, channelName string) error {
	// Get client
	client, err := c.GetClient(ctx, uuid)
	if err != nil {
		return err
	}

	// Add channel to client's channel list
	client.Channels = append(client.Channels, channelName)
	clientData, err := json.Marshal(client)
	if err != nil {
		return fmt.Errorf("failed to marshal client data: %w", err)
	}

	// Get or create channel
	channelKey := fmt.Sprintf("channel:%s", channelName)
	channelDataStr, err := c.rdb.Get(ctx, channelKey).Result()

	var channel ChannelData
	if err == redis.Nil {
		// Create new channel
		channel = ChannelData{
			Name:      channelName,
			Members:   map[string]bool{uuid: true},
			Operators: map[string]bool{uuid: true}, // First member is operator
			Modes:     "",
		}
	} else if err != nil {
		return fmt.Errorf("failed to get channel: %w", err)
	} else {
		// Update existing channel
		if err := json.Unmarshal([]byte(channelDataStr), &channel); err != nil {
			return fmt.Errorf("failed to unmarshal channel data: %w", err)
		}
		channel.Members[uuid] = true
	}

	channelData, err := json.Marshal(channel)
	if err != nil {
		return fmt.Errorf("failed to marshal channel data: %w", err)
	}

	pipe := c.rdb.Pipeline()
	pipe.Set(ctx, fmt.Sprintf("client:%s", uuid), clientData, 0)
	pipe.Set(ctx, channelKey, channelData, 0)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to join channel: %w", err)
	}

	return nil
}

// PartChannel removes a client from a channel
func (c *Client) PartChannel(ctx context.Context, uuid, channelName string) error {
	// Get client
	client, err := c.GetClient(ctx, uuid)
	if err != nil {
		return err
	}

	// Remove channel from client's channel list
	newChannels := []string{}
	for _, ch := range client.Channels {
		if ch != channelName {
			newChannels = append(newChannels, ch)
		}
	}
	client.Channels = newChannels
	clientData, err := json.Marshal(client)
	if err != nil {
		return fmt.Errorf("failed to marshal client data: %w", err)
	}

	// Get channel
	channelKey := fmt.Sprintf("channel:%s", channelName)
	channelDataStr, err := c.rdb.Get(ctx, channelKey).Result()
	if err == redis.Nil {
		return fmt.Errorf("channel not found: %s", channelName)
	}
	if err != nil {
		return fmt.Errorf("failed to get channel: %w", err)
	}

	var channel ChannelData
	if err := json.Unmarshal([]byte(channelDataStr), &channel); err != nil {
		return fmt.Errorf("failed to unmarshal channel data: %w", err)
	}

	// Remove member and operator status
	delete(channel.Members, uuid)
	delete(channel.Operators, uuid)

	pipe := c.rdb.Pipeline()
	pipe.Set(ctx, fmt.Sprintf("client:%s", uuid), clientData, 0)

	// If channel is empty, delete it; otherwise update it
	if len(channel.Members) == 0 {
		pipe.Del(ctx, channelKey)
	} else {
		channelData, err := json.Marshal(channel)
		if err != nil {
			return fmt.Errorf("failed to marshal channel data: %w", err)
		}
		pipe.Set(ctx, channelKey, channelData, 0)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to part channel: %w", err)
	}

	return nil
}

// GetChannel retrieves channel data
func (c *Client) GetChannel(ctx context.Context, channelName string) (*ChannelData, error) {
	data, err := c.rdb.Get(ctx, fmt.Sprintf("channel:%s", channelName)).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("channel not found: %s", channelName)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get channel: %w", err)
	}

	var channel ChannelData
	if err := json.Unmarshal([]byte(data), &channel); err != nil {
		return nil, fmt.Errorf("failed to unmarshal channel data: %w", err)
	}

	return &channel, nil
}

// PublishEvent publishes an event to all pods via Redis Pub/Sub
func (c *Client) PublishEvent(ctx context.Context, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	if err := c.rdb.Publish(ctx, "girc:events", data).Err(); err != nil {
		return fmt.Errorf("failed to publish event: %w", err)
	}

	return nil
}

// SubscribeEvents subscribes to events from other pods
func (c *Client) SubscribeEvents(ctx context.Context) (<-chan *Event, error) {
	c.pubsub = c.rdb.Subscribe(ctx, "girc:events")

	// Wait for subscription confirmation
	if _, err := c.pubsub.Receive(ctx); err != nil {
		return nil, fmt.Errorf("failed to subscribe: %w", err)
	}

	eventChan := make(chan *Event)

	go func() {
		defer close(eventChan)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-c.pubsub.Channel():
				if !ok {
					// Channel closed, exit gracefully
					return
				}
				if msg == nil {
					continue
				}
				var event Event
				if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
					// Log error but continue
					continue
				}
				eventChan <- &event
			}
		}
	}()

	return eventChan, nil
}

// IsNickAvailable checks if a nickname is available
func (c *Client) IsNickAvailable(ctx context.Context, nick string) (bool, error) {
	exists, err := c.rdb.Exists(ctx, fmt.Sprintf("nick:%s", nick)).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check nick availability: %w", err)
	}
	return exists == 0, nil
}

// GetPodClients returns all client UUIDs connected to this pod
func (c *Client) GetPodClients(ctx context.Context) ([]string, error) {
	members, err := c.rdb.SMembers(ctx, fmt.Sprintf("pod:%s:clients", c.podID)).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get pod clients: %w", err)
	}
	return members, nil
}

// Health checks if Redis connection is healthy
func (c *Client) Health(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// GracefulShutdown handles graceful pod shutdown by disconnecting clients
// without deleting their state. This allows clients to reconnect to other pods.
// The actual cleanup of stale client data should be handled by:
// 1. Client timeout mechanisms on other pods
// 2. Health checks that detect disconnected clients
// 3. Manual cleanup operations when all pods are stopped
func (c *Client) GracefulShutdown(ctx context.Context) error {
	// Just delete the pod's client set - don't delete client data
	// This signals to other pods that this pod is no longer serving clients
	// The clients themselves will reconnect to other available pods
	if err := c.rdb.Del(ctx, fmt.Sprintf("pod:%s:clients", c.podID)).Err(); err != nil {
		return fmt.Errorf("failed to delete pod client set: %w", err)
	}

	// Publish a pod shutdown event so other pods can be aware
	event := Event{
		Type: "POD_SHUTDOWN",
		Data: map[string]interface{}{
			"pod_id": c.podID,
		},
	}
	if err := c.PublishEvent(ctx, event); err != nil {
		// Log but don't fail - this is informational
		return fmt.Errorf("failed to publish shutdown event: %w", err)
	}

	return nil
}

// CleanupAllPods removes ALL data from Redis (use with caution!)
// This is useful when all servers are stopped and you want to reset state
func (c *Client) CleanupAllPods(ctx context.Context) error {
	// Get all keys matching our patterns
	patterns := []string{
		"client:*",
		"nick:*",
		"channel:*",
		"pod:*",
	}

	for _, pattern := range patterns {
		keys, err := c.rdb.Keys(ctx, pattern).Result()
		if err != nil {
			return fmt.Errorf("failed to get keys for pattern %s: %w", pattern, err)
		}

		if len(keys) > 0 {
			if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("failed to delete keys for pattern %s: %w", pattern, err)
			}
		}
	}

	return nil
}

// UserData represents a registered user account
type UserData struct {
	Username       string    `json:"username"`
	HashedPassword string    `json:"hashed_password"`
	Email          string    `json:"email"`
	CreatedAt      time.Time `json:"created_at"`
	LastLogin      time.Time `json:"last_login"`
}

// StoreUser stores a new user account with hashed password
func (c *Client) StoreUser(ctx context.Context, username, hashedPassword, email string) error {
	// Check if username already exists
	exists, err := c.rdb.Exists(ctx, fmt.Sprintf("user:%s", username)).Result()
	if err != nil {
		return fmt.Errorf("failed to check user existence: %w", err)
	}
	if exists > 0 {
		return fmt.Errorf("username already exists: %s", username)
	}

	// Create user data
	userData := UserData{
		Username:       username,
		HashedPassword: hashedPassword,
		Email:          email,
		CreatedAt:      time.Now(),
		LastLogin:      time.Time{}, // Zero time until first login
	}

	data, err := json.Marshal(userData)
	if err != nil {
		return fmt.Errorf("failed to marshal user data: %w", err)
	}

	// Store user data
	if err := c.rdb.Set(ctx, fmt.Sprintf("user:%s", username), data, 0).Err(); err != nil {
		return fmt.Errorf("failed to store user: %w", err)
	}

	return nil
}

// GetUser retrieves a user account by username
func (c *Client) GetUser(ctx context.Context, username string) (*UserData, error) {
	data, err := c.rdb.Get(ctx, fmt.Sprintf("user:%s", username)).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("user not found: %s", username)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	var user UserData
	if err := json.Unmarshal([]byte(data), &user); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user data: %w", err)
	}

	return &user, nil
}

// UpdateLastLogin updates the last login time for a user
func (c *Client) UpdateLastLogin(ctx context.Context, username string) error {
	// Get user
	user, err := c.GetUser(ctx, username)
	if err != nil {
		return err
	}

	// Update last login time
	user.LastLogin = time.Now()

	data, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user data: %w", err)
	}

	if err := c.rdb.Set(ctx, fmt.Sprintf("user:%s", username), data, 0).Err(); err != nil {
		return fmt.Errorf("failed to update last login: %w", err)
	}

	return nil
}

// UpdatePassword updates a user's password with a new hashed password
func (c *Client) UpdatePassword(ctx context.Context, username, newHashedPassword string) error {
	// Get user
	user, err := c.GetUser(ctx, username)
	if err != nil {
		return err
	}

	// Update password
	user.HashedPassword = newHashedPassword

	data, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user data: %w", err)
	}

	if err := c.rdb.Set(ctx, fmt.Sprintf("user:%s", username), data, 0).Err(); err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	return nil
}

// RegisterChannel registers a channel with founder and metadata
func (c *Client) RegisterChannel(ctx context.Context, channelName, founderAccount, description string) error {
	// Check if channel already registered
	existingData, err := c.GetChannel(ctx, channelName)
	if err == nil && existingData != nil && existingData.Registered {
		return fmt.Errorf("channel already registered")
	}

	// Create or update channel data
	channelData := &ChannelData{
		Name:         channelName,
		Registered:   true,
		Founder:      founderAccount,
		RegisteredAt: time.Now().Unix(),
		Description:  description,
	}

	// If channel data exists, preserve existing fields
	if existingData != nil {
		channelData.Topic = existingData.Topic
		channelData.Modes = existingData.Modes
		channelData.Members = existingData.Members
		channelData.Operators = existingData.Operators
	}

	data, err := json.Marshal(channelData)
	if err != nil {
		return fmt.Errorf("failed to marshal channel data: %w", err)
	}

	// Store in Redis with no expiration (persistent)
	if err := c.rdb.Set(ctx, fmt.Sprintf("channel:%s", channelName), data, 0).Err(); err != nil {
		return fmt.Errorf("failed to register channel: %w", err)
	}

	return nil
}

// GetChannelRegistration retrieves registration info for a channel
func (c *Client) GetChannelRegistration(ctx context.Context, channelName string) (*ChannelData, error) {
	data, err := c.rdb.Get(ctx, fmt.Sprintf("channel:%s", channelName)).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("channel not registered: %s", channelName)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get channel: %w", err)
	}

	var channel ChannelData
	if err := json.Unmarshal([]byte(data), &channel); err != nil {
		return nil, fmt.Errorf("failed to unmarshal channel data: %w", err)
	}

	return &channel, nil
}

// IsChannelRegistered checks if a channel is registered
func (c *Client) IsChannelRegistered(ctx context.Context, channelName string) (bool, error) {
	channel, err := c.GetChannelRegistration(ctx, channelName)
	if err != nil {
		return false, nil // Not registered or error - treat as not registered
	}
	return channel.Registered, nil
}
