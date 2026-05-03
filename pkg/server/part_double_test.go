package server

import (
	"strings"
	"testing"
	"time"
)

// TestPARTReceivedOnce verifies that the parting client receives the PART line
// exactly once (regression for the double-send bug fixed in iter 2).
func TestPARTReceivedOnce(t *testing.T) {
	addr := startTestServer(t)

	a := dialServer(t, addr)
	b := dialServer(t, addr)

	register(t, a, "parterA")
	time.Sleep(50 * time.Millisecond)
	register(t, b, "parterB")
	time.Sleep(50 * time.Millisecond)

	a.send(t, "JOIN #doublepart")
	a.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "doublepart") })
	b.send(t, "JOIN #doublepart")
	b.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "doublepart") })

	// a PARTs; collect all lines from a for 300 ms.
	a.send(t, "PART #doublepart :bye")

	count := 0
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		a.conn.SetReadDeadline(deadline)
		line, err := a.reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, "PART") && strings.Contains(line, "doublepart") {
			count++
		}
	}

	if count == 0 {
		t.Error("parting client did not receive PART confirmation")
	}
	if count > 1 {
		t.Errorf("parting client received PART %d times (expected exactly 1)", count)
	}
}
