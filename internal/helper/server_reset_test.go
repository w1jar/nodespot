package helper

import (
	"context"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// A request is written as JSON plus "\n". The server's json.Decoder stops at
// the closing brace, so depending on where its read buffer boundaries fall the
// newline stays unread in the receive buffer. Closing a unix stream socket
// with unread data resets the peer, which then loses the reply ("connection
// reset by peer"). Every reply must arrive for every request size.
func TestServerNoResetAroundBufferBoundaries(t *testing.T) {
	c, _ := startServer(t, &Ops{Run: &fakeRunner{}}, PeerUID, []uint32{0, uint32(os.Getuid())})
	var sizes []int
	for _, b := range []int{512, 1024, 2048, 4096} {
		for n := b - 40; n <= b+40; n++ {
			sizes = append(sizes, n)
		}
	}
	for _, n := range sizes {
		// an unknown op of n bytes: answered with "unknown op" (after the
		// request is fully decoded), whatever its length
		want := "unknown op"
		if n+len(`{"op":""}`) > maxRequest {
			want = "invalid request" // over the plain-op cap: refused, still answered
		}
		// raw client: request plus newline, write side left open (the
		// server alone must prevent the reset)
		if got := rawReply(t, c.Socket, `{"op":"`+strings.Repeat("x", n)+"\"}\n"); !strings.Contains(got, want) {
			t.Fatalf("raw, op length %d: want %q, got %q", n, want, got)
		}
		// the real client
		err := c.Do(context.Background(), strings.Repeat("x", n), nil)
		if err == nil || err.Error() != want {
			t.Fatalf("client, op length %d: want %q, got %v", n, want, err)
		}
	}
}

// rawReply sends req and returns the reply, failing the test on a read error
// (a connection reset among them).
func rawReply(t *testing.T, sock, req string) string {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("reading the reply: %v", err)
	}
	return string(data)
}

// Rejection paths must not reset either: a bad request with trailing bytes and
// an unauthorized peer.
func TestServerRejectionsDoNotReset(t *testing.T) {
	deny := func(net.Conn) (uint32, error) { return 4242, nil }
	c, _ := startServer(t, &Ops{Run: &fakeRunner{}}, deny, []uint32{0})
	for n := 500; n < 530; n++ {
		if got := rawReply(t, c.Socket, `{"op":"`+strings.Repeat("x", n)+"\"}\n"); !strings.Contains(got, "not authorized") {
			t.Fatalf("unauthorized, length %d: got %q", n, got)
		}
		if err := c.Do(context.Background(), strings.Repeat("x", n), nil); err == nil || err.Error() != "not authorized" {
			t.Fatalf("unauthorized, client, length %d: got %v", n, err)
		}
	}
	for _, raw := range []string{"{\"op\":\n", "not json at all\n", "{\"op\":\"x\",\"bogus\":1}\n" + strings.Repeat(" ", 3000)} {
		conn, err := net.Dial("unix", c.Socket)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = conn.Write([]byte(raw))
		if strings.HasPrefix(raw, "{\"op\":\n") {
			_ = conn.(*net.UnixConn).CloseWrite() // truncated JSON: the server needs the EOF
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 256)
		n, err := conn.Read(buf)
		conn.Close()
		if err != nil || !strings.Contains(string(buf[:n]), "invalid request") {
			t.Fatalf("bad request %q: %q %v", raw[:5], buf[:n], err)
		}
	}
}

// A client that sends nothing after the request (no newline, no close) must
// still get its reply promptly.
func TestServerSilentClientNotBlocked(t *testing.T) {
	c, _ := startServer(t, &Ops{Run: &fakeRunner{}}, PeerUID, []uint32{0, uint32(os.Getuid())})
	conn, err := net.Dial("unix", c.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(`{"op":"nope"}`))
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 64)
	if n, err := conn.Read(buf); err != nil || !strings.Contains(string(buf[:n]), "unknown op") {
		t.Fatalf("%q %v", buf[:n], err)
	}
}
