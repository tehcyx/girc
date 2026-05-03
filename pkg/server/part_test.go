package server

import (
	"strings"
	"testing"
	"time"
)

// TestPARTScopeChannelMembers verifies that a PART message is sent to channel
// members only, not to clients who are not in the channel.
func TestPARTScopeChannelMembers(t *testing.T) {
	addr := startTestServer(t)

	member := dialServer(t, addr)
	parter := dialServer(t, addr)
	outsider := dialServer(t, addr)

	// Register sequentially to avoid concurrent JoinRoomByName race.
	register(t, parter, "partscoper")
	time.Sleep(50 * time.Millisecond)
	register(t, member, "partmember")
	time.Sleep(50 * time.Millisecond)
	register(t, outsider, "partoutsider")
	time.Sleep(50 * time.Millisecond)

	// parter and member join #partscope; outsider does not.
	parter.send(t, "JOIN #partscope")
	parter.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "partscope") })
	member.send(t, "JOIN #partscope")
	member.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "partscope") })

	// Drain any pending messages for outsider.
	time.Sleep(100 * time.Millisecond)

	parter.send(t, "PART #partscope :bye")

	// parter themselves should see the PART.
	parter.readUntil(t, func(l string) bool {
		return strings.Contains(l, "PART") && strings.Contains(l, "partscope")
	})

	// member should see the PART.
	member.readUntil(t, func(l string) bool {
		return strings.Contains(l, "PART") && strings.Contains(l, "partscope")
	})

	// outsider must NOT see the PART within 200 ms.
	gotPart := false
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		outsider.conn.SetReadDeadline(deadline)
		line, err := outsider.reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, "PART") && strings.Contains(line, "partscope") {
			gotPart = true
			break
		}
	}
	if gotPart {
		t.Error("outsider received PART for a channel they are not in")
	}
}

// TestPARTMultiChannelRightMembers verifies that when a client PARTs two
// channels in one command, the PART notification for each channel reaches
// only the members of that specific channel.
func TestPARTMultiChannelRightMembers(t *testing.T) {
	addr := startTestServer(t)

	alpha := dialServer(t, addr) // member of #alpha only
	beta := dialServer(t, addr)  // member of #beta only
	parter := dialServer(t, addr)

	// Register one at a time to avoid triggering a known race in JoinRoomByName
	// when multiple clients register concurrently (concurrent AddClient + JoinRoomByName
	// can race on the s.Clients slice pointer — a production code bug).
	register(t, parter, "multipart")
	time.Sleep(50 * time.Millisecond)
	register(t, alpha, "alphamember")
	time.Sleep(50 * time.Millisecond)
	register(t, beta, "betamember")
	time.Sleep(50 * time.Millisecond)

	// parter joins both channels.
	parter.send(t, "JOIN #alpha")
	parter.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "alpha") })
	parter.send(t, "JOIN #beta")
	parter.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "beta") })

	// alpha joins #alpha.
	alpha.send(t, "JOIN #alpha")
	alpha.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "alpha") })

	// beta joins #beta.
	beta.send(t, "JOIN #beta")
	beta.readUntil(t, func(l string) bool { return strings.Contains(l, "JOIN") && strings.Contains(l, "beta") })

	time.Sleep(50 * time.Millisecond)

	// Part both channels at once.
	parter.send(t, "PART #alpha,#beta :leaving")

	// alpha should see PART for #alpha.
	alpha.readUntil(t, func(l string) bool {
		return strings.Contains(l, "PART") && strings.Contains(l, "alpha")
	})

	// beta should see PART for #beta.
	beta.readUntil(t, func(l string) bool {
		return strings.Contains(l, "PART") && strings.Contains(l, "beta")
	})

	// alpha should NOT see PART for #beta within 200 ms.
	gotBeta := false
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		alpha.conn.SetReadDeadline(deadline)
		line, err := alpha.reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, "PART") && strings.Contains(line, "beta") {
			gotBeta = true
			break
		}
	}
	if gotBeta {
		t.Errorf("alpha member received PART for #beta (a channel they are not in)")
	}

	// beta should NOT see PART for #alpha within 200 ms.
	gotAlpha := false
	deadline = time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		beta.conn.SetReadDeadline(deadline)
		line, err := beta.reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, "PART") && strings.Contains(line, "alpha") {
			gotAlpha = true
			break
		}
	}
	if gotAlpha {
		t.Error("beta member received PART for #alpha (a channel they are not in)")
	}
}
