package server

import (
	"strings"
	"testing"
	"time"
)

// TestJoinTopicNonEmpty verifies that when a channel has a topic set,
// a client who JOINs receives 332 RPL_TOPIC (not 331 RPL_NOTOPIC).
// This is the companion to TestJoinTopicNumerics (empty topic → 331).
func TestJoinTopicNonEmpty(t *testing.T) {
	addr := startTestServer(t)

	// First client creates the channel and sets a topic.
	c1 := dialServer(t, addr)
	register(t, c1, "topicsetter")

	c1.send(t, "JOIN #topiced")
	c1.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "topiced") })
	c1.send(t, "TOPIC #topiced :Hello world")
	c1.readUntil(t, func(l string) bool { return strings.Contains(l, "TOPIC") })

	// Give the server time to finish before second client connects.
	time.Sleep(50 * time.Millisecond)

	// Second client joins and should see 332 RPL_TOPIC.
	c2 := dialServer(t, addr)
	register(t, c2, "topicjoiner")

	c2.send(t, "JOIN #topiced")

	var topicLine string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c2.conn.SetReadDeadline(deadline)
		line, err := c2.reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if (strings.Contains(line, " 331 ") || strings.Contains(line, " 332 ")) && strings.Contains(line, "topiced") {
			topicLine = line
			break
		}
	}

	if !strings.Contains(topicLine, " 332 ") {
		t.Errorf("expected 332 RPL_TOPIC for non-empty topic, got: %q", topicLine)
	}
	if !strings.Contains(topicLine, "Hello world") {
		t.Errorf("expected topic 'Hello world' in 332, got: %q", topicLine)
	}
}
