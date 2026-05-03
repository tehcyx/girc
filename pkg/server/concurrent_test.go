package server

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// registerNoFatal connects to addr, registers with nick, and waits for 001.
// Returns a live ircConn and "" on success, or nil and an error string on failure.
// Safe to call from goroutines because it never calls t.Fatal.
func registerNoFatal(addr, nick string) (*ircConn, string) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Sprintf("dial: %v", err)
	}
	r := bufio.NewReader(conn)
	fmt.Fprintf(conn, "NICK %s\r\n", nick)
	fmt.Fprintf(conn, "USER %s 0 * :Test User\r\n", nick)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		line, err := r.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, fmt.Sprintf("read during registration: %v", err)
		}
		if strings.Contains(line, " 001 ") {
			return &ircConn{conn: conn, reader: r}, ""
		}
	}
	conn.Close()
	return nil, "timed out waiting for 001"
}

// TestConcurrentJoinSameChannel stress-tests JoinRoomByName by registering
// clients sequentially and then having them all JOIN the same channel
// concurrently. Run with -race.
func TestConcurrentJoinSameChannel(t *testing.T) {
	addr := startTestServer(t)

	// n=8: enough to exercise concurrency without leaving so many lingering
	// goroutines that subsequent tests become flaky.
	const n = 8
	// Register all clients sequentially first — this avoids triggering the
	// clientPtr-invalidation race in server.go that is orthogonal to the
	// JoinRoomByName fix under test.
	conns := make([]*ircConn, n)
	for i := 0; i < n; i++ {
		c, errStr := registerNoFatal(addr, fmt.Sprintf("racer%d", i))
		if errStr != "" {
			t.Fatalf("racer%d register: %s", i, errStr)
		}
		t.Cleanup(func() { c.conn.Close() })
		conns[i] = c
	}

	// All JOIN concurrently.
	var wg sync.WaitGroup
	errCh := make(chan string, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int, c *ircConn) {
			defer wg.Done()

			fmt.Fprintf(c.conn, "JOIN #racechan\r\n")

			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				c.conn.SetReadDeadline(deadline)
				line, err := c.reader.ReadString('\n')
				if err != nil {
					errCh <- fmt.Sprintf("racer%d read error after JOIN: %v", idx, err)
					return
				}
				if strings.Contains(line, "JOIN") && strings.Contains(line, "racechan") {
					return
				}
				if strings.Contains(line, "366") { // RPL_ENDOFNAMES
					return
				}
			}
			errCh <- fmt.Sprintf("racer%d: timed out waiting for JOIN confirmation", idx)
		}(i, conns[i])
	}

	wg.Wait()

	// Close all connections explicitly so server goroutines can start winding
	// down before the next test's goroutines begin. Without this, lingering
	// goroutines from this test can race with subsequent tests under -race.
	for _, c := range conns {
		if c != nil {
			c.conn.Close()
		}
	}
	// Brief pause to allow server-side cleanup goroutines to complete.
	time.Sleep(200 * time.Millisecond)

	close(errCh)

	for msg := range errCh {
		t.Error(msg)
	}
}

// TestConcurrentPrivmsg stress-tests PRIVMSG by having multiple senders
// simultaneously send to the same channel. Run with -race.
// Registrations and JOINs are sequential; only the actual sends are concurrent.
func TestConcurrentPrivmsg(t *testing.T) {
	addr := startTestServer(t)

	const senders = 10
	conns := make([]*ircConn, senders)
	for i := 0; i < senders; i++ {
		c := dialServer(t, addr)
		register(t, c, fmt.Sprintf("pmsgr%d", i))
		conns[i] = c
	}

	for _, c := range conns {
		c.send(t, "JOIN #privmsgrace")
		c.readUntil(t, func(l string) bool {
			return strings.Contains(l, "JOIN") && strings.Contains(l, "privmsgrace")
		})
	}

	// All send simultaneously. Use fmt.Fprintf directly to avoid t.Fatal in goroutine.
	var wg sync.WaitGroup
	for i, c := range conns {
		wg.Add(1)
		go func(idx int, conn *ircConn) {
			defer wg.Done()
			conn.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			fmt.Fprintf(conn.conn, "PRIVMSG #privmsgrace :hello from %d\r\n", idx)
		}(i, c)
	}
	wg.Wait()
	// No assertions beyond no panic / no data race detected by -race.
}

// TestConcurrentJoinDistinctChannels stress-tests the roomPtr fix in
// JoinRoomByName: when N goroutines each create a brand-new channel, every
// append to s.Rooms can reallocate the backing array, making any raw *Room
// captured before the append stale.  Run with -race.
func TestConcurrentJoinDistinctChannels(t *testing.T) {
	addr := startTestServer(t)

	const n = 16
	conns := make([]*ircConn, n)
	for i := 0; i < n; i++ {
		c, errStr := registerNoFatal(addr, fmt.Sprintf("distjoin%d", i))
		if errStr != "" {
			t.Fatalf("distjoin%d register: %s", i, errStr)
		}
		t.Cleanup(func() { c.conn.Close() })
		conns[i] = c
	}

	// Each goroutine joins its own unique channel, forcing s.Rooms to grow
	// on every JOIN and potentially reallocating the slice backing array.
	var wg sync.WaitGroup
	errCh := make(chan string, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int, c *ircConn) {
			defer wg.Done()
			chanName := fmt.Sprintf("#distinct%d", idx)
			fmt.Fprintf(c.conn, "JOIN %s\r\n", chanName)

			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				c.conn.SetReadDeadline(deadline)
				line, err := c.reader.ReadString('\n')
				if err != nil {
					errCh <- fmt.Sprintf("distjoin%d read error: %v", idx, err)
					return
				}
				if strings.Contains(line, "JOIN") && strings.Contains(line, fmt.Sprintf("distinct%d", idx)) {
					return
				}
				if strings.Contains(line, "366") {
					return
				}
			}
			errCh <- fmt.Sprintf("distjoin%d: timed out waiting for JOIN #distinct%d", idx, idx)
		}(i, conns[i])
	}

	wg.Wait()

	for _, c := range conns {
		if c != nil {
			c.conn.Close()
		}
	}
	time.Sleep(200 * time.Millisecond)

	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
}

// TestConcurrentKickNotPanic stress-tests the KICK path by having the channel
// operator kick multiple victims concurrently. Run with -race.
// Only the KICK sends themselves are concurrent; setup is sequential.
func TestConcurrentKickNotPanic(t *testing.T) {
	addr := startTestServer(t)

	const targets = 5
	op := dialServer(t, addr)
	register(t, op, "kickop")

	op.send(t, "JOIN #kickrace")
	op.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "kickrace") })

	for i := 0; i < targets; i++ {
		c := dialServer(t, addr)
		register(t, c, fmt.Sprintf("victim%d", i))
		c.send(t, "JOIN #kickrace")
		c.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "kickrace") })
	}

	// Kick all victims concurrently. Use fmt.Fprintf to avoid t.Fatal in goroutine.
	var wg sync.WaitGroup
	for i := 0; i < targets; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			op.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			fmt.Fprintf(op.conn, "KICK #kickrace victim%d :bye\r\n", idx)
		}(i)
	}
	wg.Wait()
	// Success if no race and no panic.
}

// TestConcurrentNoticeRace is a regression test for the NOTICE snapshot fix
// (A5 in iter 3): the NOTICE channel handler previously iterated s.Clients
// under roomMux only, racing with concurrent AddClient/RemoveClient which
// requires ClientsMutex.  This test sends NOTICE messages while clients are
// simultaneously connecting and disconnecting.  Run with -race.
func TestConcurrentNoticeRace(t *testing.T) {
	addr := startTestServer(t)

	// Establish a stable sender and a set of stable channel members.
	const stable = 4
	sender := dialServer(t, addr)
	register(t, sender, "noticesender")
	sender.send(t, "JOIN #noticerace")
	sender.readUntil(t, func(l string) bool {
		return strings.Contains(l, "JOIN") && strings.Contains(l, "noticerace")
	})

	stableConns := make([]*ircConn, stable)
	for i := 0; i < stable; i++ {
		c := dialServer(t, addr)
		register(t, c, fmt.Sprintf("noticemember%d", i))
		c.send(t, "JOIN #noticerace")
		c.readUntil(t, func(l string) bool {
			return strings.Contains(l, "JOIN") && strings.Contains(l, "noticerace")
		})
		stableConns[i] = c
	}

	// Goroutine A: send NOTICE to the channel rapidly.
	var senderWg sync.WaitGroup
	senderWg.Add(1)
	go func() {
		defer senderWg.Done()
		for i := 0; i < 40; i++ {
			sender.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			fmt.Fprintf(sender.conn, "NOTICE #noticerace :stress %d\r\n", i)
		}
	}()

	// Goroutine B: repeatedly connect, register, join and immediately disconnect
	// — this drives AddClient/RemoveClient concurrently with the NOTICE sends.
	var connWg sync.WaitGroup
	for i := 0; i < 8; i++ {
		connWg.Add(1)
		go func(idx int) {
			defer connWg.Done()
			conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				return
			}
			r := bufio.NewReader(conn)
			nick := fmt.Sprintf("transient%d", idx)
			fmt.Fprintf(conn, "NICK %s\r\nUSER %s 0 * :T\r\n", nick, nick)
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				conn.SetReadDeadline(deadline)
				line, err := r.ReadString('\n')
				if err != nil {
					break
				}
				if strings.Contains(line, " 001 ") {
					fmt.Fprintf(conn, "JOIN #noticerace\r\n")
					break
				}
			}
			// Disconnect immediately — triggers RemoveClient concurrently.
			conn.Close()
		}(i)
	}

	senderWg.Wait()
	connWg.Wait()
	// Success criterion: -race detector reports no data races.
}
