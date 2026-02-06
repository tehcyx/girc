package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Pod Coordination & Health Management
// These methods enable distributed coordination between multiple server pods

// RegisterPod registers this pod in the pod registry with initial heartbeat
func (c *Client) RegisterPod(ctx context.Context, version string) error {
	podInfo := PodInfo{
		PodID:         c.podID,
		StartTime:     time.Now(),
		LastHeartbeat: time.Now(),
		ClientCount:   0,
		Version:       version,
	}

	data, err := json.Marshal(podInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal pod info: %w", err)
	}

	// Store pod info with 30 second TTL (will be refreshed by heartbeat)
	if err := c.rdb.Set(ctx, fmt.Sprintf("pod:%s:info", c.podID), data, 30*time.Second).Err(); err != nil {
		return fmt.Errorf("failed to register pod: %w", err)
	}

	// Add to active pods set
	if err := c.rdb.SAdd(ctx, "pods:active", c.podID).Err(); err != nil {
		return fmt.Errorf("failed to add pod to active set: %w", err)
	}

	return nil
}

// Heartbeat sends a heartbeat to signal this pod is still alive
// Should be called periodically (e.g., every 10 seconds)
func (c *Client) Heartbeat(ctx context.Context, clientCount int, version string) error {
	// Get pod clients count from Redis (more accurate than local count)
	members, err := c.rdb.SMembers(ctx, fmt.Sprintf("pod:%s:clients", c.podID)).Result()
	if err == nil {
		clientCount = len(members)
	}

	podInfo := PodInfo{
		PodID:         c.podID,
		LastHeartbeat: time.Now(),
		ClientCount:   clientCount,
		Version:       version,
	}

	// Preserve StartTime if it exists
	existingData, err := c.rdb.Get(ctx, fmt.Sprintf("pod:%s:info", c.podID)).Result()
	if err == nil {
		var existing PodInfo
		if err := json.Unmarshal([]byte(existingData), &existing); err == nil {
			podInfo.StartTime = existing.StartTime
		}
	}

	data, err := json.Marshal(podInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal pod info: %w", err)
	}

	// Refresh TTL to 30 seconds
	if err := c.rdb.Set(ctx, fmt.Sprintf("pod:%s:info", c.podID), data, 30*time.Second).Err(); err != nil {
		return fmt.Errorf("failed to send heartbeat: %w", err)
	}

	// Publish heartbeat event (optional, for monitoring)
	event := Event{
		Type: "POD_HEARTBEAT",
		Data: map[string]interface{}{
			"pod_id":       c.podID,
			"client_count": clientCount,
			"version":      version,
		},
	}
	// Don't fail on publish error - heartbeat is still recorded
	_ = c.PublishEvent(ctx, event)

	return nil
}

// GetActivePods returns list of all active pods (that have sent heartbeat recently)
func (c *Client) GetActivePods(ctx context.Context) ([]PodInfo, error) {
	podIDs, err := c.rdb.SMembers(ctx, "pods:active").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get active pods: %w", err)
	}

	var pods []PodInfo
	now := time.Now()

	for _, podID := range podIDs {
		data, err := c.rdb.Get(ctx, fmt.Sprintf("pod:%s:info", podID)).Result()
		if err == goredis.Nil {
			// Pod info expired, remove from active set
			c.rdb.SRem(ctx, "pods:active", podID)
			continue
		}
		if err != nil {
			continue // Skip this pod
		}

		var podInfo PodInfo
		if err := json.Unmarshal([]byte(data), &podInfo); err != nil {
			continue
		}

		// Check if heartbeat is recent (within 60 seconds)
		if now.Sub(podInfo.LastHeartbeat) < 60*time.Second {
			pods = append(pods, podInfo)
		} else {
			// Pod is stale, remove from active set
			c.rdb.SRem(ctx, "pods:active", podID)
		}
	}

	return pods, nil
}

// FindOrphanedClients finds clients not connected to any active pod
func (c *Client) FindOrphanedClients(ctx context.Context) ([]ClientData, error) {
	// Get all active pods
	activePods, err := c.GetActivePods(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get active pods: %w", err)
	}

	activePodMap := make(map[string]bool)
	for _, pod := range activePods {
		activePodMap[pod.PodID] = true
	}

	// Get all client keys
	keys, err := c.rdb.Keys(ctx, "client:*").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get client keys: %w", err)
	}

	var orphaned []ClientData
	for _, key := range keys {
		data, err := c.rdb.Get(ctx, key).Result()
		if err != nil {
			continue
		}

		var client ClientData
		if err := json.Unmarshal([]byte(data), &client); err != nil {
			continue
		}

		// Check if client's pod is active
		if !activePodMap[client.PodID] {
			orphaned = append(orphaned, client)
		}
	}

	return orphaned, nil
}

// CleanupOrphanedClients removes clients that are not connected to any active pod
// Returns the number of clients cleaned up
func (c *Client) CleanupOrphanedClients(ctx context.Context) (int, error) {
	orphaned, err := c.FindOrphanedClients(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to find orphaned clients: %w", err)
	}

	count := 0
	for _, client := range orphaned {
		// Unregister the orphaned client
		pipe := c.rdb.Pipeline()
		pipe.Del(ctx, fmt.Sprintf("client:%s", client.UUID))
		pipe.Del(ctx, fmt.Sprintf("nick:%s", client.Nick))
		pipe.SRem(ctx, fmt.Sprintf("pod:%s:clients", client.PodID), client.UUID)

		// Remove from channels
		for _, channelName := range client.Channels {
			channelKey := fmt.Sprintf("channel:%s", channelName)
			channelData, err := c.rdb.Get(ctx, channelKey).Result()
			if err == nil {
				var channel ChannelData
				if err := json.Unmarshal([]byte(channelData), &channel); err == nil {
					delete(channel.Members, client.UUID)
					delete(channel.Operators, client.UUID)

					if len(channel.Members) == 0 {
						pipe.Del(ctx, channelKey)
					} else {
						updatedData, _ := json.Marshal(channel)
						pipe.Set(ctx, channelKey, updatedData, 0)
					}
				}
			}
		}

		if _, err := pipe.Exec(ctx); err != nil {
			// Log error but continue
			continue
		}
		count++
	}

	return count, nil
}

// AcquireLeaderLock attempts to acquire a distributed lock for leader election
// Returns true if lock was acquired, false otherwise
// The lock has a TTL to prevent deadlocks if the leader crashes
func (c *Client) AcquireLeaderLock(ctx context.Context, ttl time.Duration) (bool, error) {
	// Try to set the leader lock with NX (only if not exists)
	result, err := c.rdb.SetNX(ctx, "lock:leader", c.podID, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("failed to acquire leader lock: %w", err)
	}
	return result, nil
}

// ReleaseLeaderLock releases the leader lock if this pod holds it
// Uses a Lua script to ensure atomicity (only delete if we own it)
func (c *Client) ReleaseLeaderLock(ctx context.Context) error {
	// Only delete if we own the lock
	script := `
		if redis.call("get", KEYS[1]) == ARGV[1] then
			return redis.call("del", KEYS[1])
		else
			return 0
		end
	`
	if err := c.rdb.Eval(ctx, script, []string{"lock:leader"}, c.podID).Err(); err != nil {
		return fmt.Errorf("failed to release leader lock: %w", err)
	}
	return nil
}

// IsLeader checks if this pod is currently the leader
func (c *Client) IsLeader(ctx context.Context) (bool, error) {
	leaderID, err := c.rdb.Get(ctx, "lock:leader").Result()
	if err == goredis.Nil {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check leader status: %w", err)
	}
	return leaderID == c.podID, nil
}
