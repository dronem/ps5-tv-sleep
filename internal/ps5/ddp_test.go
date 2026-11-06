package ps5

import (
	"context"
	"net"
	"testing"
	"time"
)

// fakePS5 answers DDP searches from a local UDP socket. Each reply in replies
// is used once, in order; an empty string drops that request.
func fakePS5(t *testing.T, replies ...string) (addr string, received chan string) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	received = make(chan string, len(replies)+8)
	go func() {
		buf := make([]byte, 2048)
		for i := 0; ; i++ {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			received <- string(buf[:n])
			if i < len(replies) && replies[i] != "" {
				conn.WriteTo([]byte(replies[i]), from)
			}
		}
	}()
	return conn.LocalAddr().String(), received
}

func newTestProber(addr string, attempts int) Prober {
	return Prober{Addr: addr, Timeout: 100 * time.Millisecond, Attempts: attempts}
}

func TestProbeStates(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		want  State
	}{
		{"on", "HTTP/1.1 200 Ok\nhost-id:ABC\nhost-type:PS5\n", On},
		{"standby", "HTTP/1.1 620 Server Standby\nhost-id:ABC\nhost-type:PS5\n", Standby},
		{"crlf", "HTTP/1.1 620 Server Standby\r\nhost-id:ABC\r\n", Standby},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, received := fakePS5(t, tt.reply)
			got, err := newTestProber(addr, 1).Probe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("state = %v, want %v", got, tt.want)
			}
			if req := <-received; req != ddpSearch {
				t.Errorf("request = %q, want %q", req, ddpSearch)
			}
		})
	}
}

func TestProbeRetriesAfterLostPacket(t *testing.T) {
	addr, _ := fakePS5(t, "", "HTTP/1.1 200 Ok\n")
	got, err := newTestProber(addr, 3).Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != On {
		t.Errorf("state = %v, want on", got)
	}
}

func TestProbeSilenceIsUnreachableWithoutError(t *testing.T) {
	addr, received := fakePS5(t)
	got, err := newTestProber(addr, 2).Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != Unreachable {
		t.Errorf("state = %v, want unreachable", got)
	}
	if len(received) != 2 {
		t.Errorf("sent %d requests, want 2", len(received))
	}
}

func TestProbeMalformedReply(t *testing.T) {
	for _, reply := range []string{"garbage", "HTTP/1.1 500 Nope\n"} {
		addr, _ := fakePS5(t, reply)
		got, err := newTestProber(addr, 1).Probe(context.Background())
		if err == nil {
			t.Errorf("reply %q: expected error", reply)
		}
		if got != Garbled {
			t.Errorf("reply %q: state = %v, want garbled", reply, got)
		}
	}
}
