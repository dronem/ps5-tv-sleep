// Package ps5 reads the power state of a PlayStation 5 over the network and
// detects its power transitions.
package ps5

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	// DDPPort is the UDP port the console answers Device Discovery Protocol on.
	DDPPort   = 9302
	ddpSearch = "SRCH * HTTP/1.1\ndevice-discovery-protocol-version:00030010\n"
)

// State is the power state reported by the console.
type State int

// The states a console can be in.
const (
	Unreachable State = iota
	On
	Standby
	// Garbled is a reply that is not a DDP status. A PS5 sends these for a few
	// seconds around a power change, so it says more than plain silence does.
	Garbled
)

// String returns the state's name as it appears in the log.
func (s State) String() string {
	switch s {
	case On:
		return "on"
	case Standby:
		return "standby"
	case Garbled:
		return "garbled"
	default:
		return "unreachable"
	}
}

// Prober asks a PS5 for its state over the Device Discovery Protocol.
type Prober struct {
	Addr     string
	Timeout  time.Duration // per attempt
	Attempts int           // UDP is lossy, so one lost packet must not look like an outage
}

// Probe returns Unreachable with a nil error when the console simply does
// not answer, and Garbled with an error when the reply is malformed. Any other
// error means a misconfigured address.
func (p Prober) Probe(ctx context.Context) (State, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", p.Addr)
	if err != nil {
		return Unreachable, fmt.Errorf("dial %s: %w", p.Addr, err)
	}
	defer conn.Close()

	buf := make([]byte, 2048)
	for range p.Attempts {
		if ctx.Err() != nil {
			return Unreachable, ctx.Err()
		}
		if err := conn.SetDeadline(time.Now().Add(p.Timeout)); err != nil {
			return Unreachable, err
		}
		if _, err := conn.Write([]byte(ddpSearch)); err != nil {
			continue
		}
		n, err := conn.Read(buf)
		if err != nil {
			continue
		}
		return parseDDPResponse(string(buf[:n]))
	}
	return Unreachable, nil
}

// parseDDPResponse reads the status line, e.g. "HTTP/1.1 620 Server Standby".
func parseDDPResponse(resp string) (State, error) {
	line, _, _ := strings.Cut(resp, "\n")
	fields := strings.Fields(line)
	if len(fields) >= 2 && strings.HasPrefix(fields[0], "HTTP/") {
		switch fields[1] {
		case "200":
			return On, nil
		case "620":
			return Standby, nil
		}
	}
	return Garbled, fmt.Errorf("unexpected ddp response %q", line)
}
