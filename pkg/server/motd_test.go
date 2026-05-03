package server

import (
	"strings"
	"testing"
	"time"
)

// TestMOTDFullSequence verifies that the welcome burst includes 001, 004
// (RPL_MYINFO), 005 (RPL_ISUPPORT), and 376 (RPL_ENDOFMOTD) in that order.
func TestMOTDFullSequence(t *testing.T) {
	addr := startTestServer(t)
	c := dialServer(t, addr)

	c.send(t, "NICK motdfull")
	c.send(t, "USER motdfull 0 * :MOTD Full")

	var (
		got001 bool
		got004 bool
		got005 bool
		got376 bool
	)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.conn.SetReadDeadline(deadline)
		line, err := c.reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.Contains(line, " 001 "):
			got001 = true
		case strings.Contains(line, " 004 "):
			got004 = true
		case strings.Contains(line, " 005 "):
			got005 = true
		case strings.Contains(line, " 376 "):
			got376 = true
		}
		if got001 && got004 && got005 && got376 {
			break
		}
	}

	if !got001 {
		t.Error("did not receive 001 RPL_WELCOME")
	}
	if !got004 {
		t.Error("did not receive 004 RPL_MYINFO")
	}
	if !got005 {
		t.Error("did not receive 005 RPL_ISUPPORT")
	}
	if !got376 {
		t.Error("did not receive 376 RPL_ENDOFMOTD")
	}
}
