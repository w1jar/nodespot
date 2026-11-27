package echolink

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

var testList = stationList(
	[4]string{"N1CALL", "Example Town [ON 12:34]", "1997", "127.0.0.2"},
	[4]string{"*EXAMPLE*", "Example conference [ON 00:01]", "1998", "127.0.0.2"},
	[4]string{"N0CALL-L", "Ourselves [ON 00:02]", "1999", "127.0.0.1"},
)

func testConfig(clk *fakeClock, servers ...string) Config {
	return Config{
		Callsign: "n0call-l",
		Password: "secret",
		Name:     "Test Op",
		Location: "Example City",
		Servers:  servers,
		Clock:    clk,
		Refresh:  5 * time.Minute,
		Retry:    30 * time.Second,
	}
}

// deadAddr returns a loopback address with nothing listening.
func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	ln.Close()
	return a
}

func TestLogin(t *testing.T) {
	tests := []struct {
		name      string
		reply     string
		deadFirst bool
		wantErr   error
		wantTries int // logons the live server sees
	}{
		{"accepted", "OK2.6\n", false, nil, 1},
		{"failover", "OK\n", true, nil, 1},
		{"bad password", "BAD PASSWORD\n", false, ErrBadLogon, 1},
		{"garbage", "WHAT\n", false, ErrBadReply, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fd := newFakeDirectory(t, testList)
			fd.reply = tc.reply
			servers := []string{fd.addr()}
			if tc.deadFirst {
				servers = []string{deadAddr(t), fd.addr()}
			}
			clk := newFakeClock()
			d, err := Login(context.Background(), testConfig(clk, servers...))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if err != nil && strings.Contains(err.Error(), "secret") {
					t.Fatal("error leaks the password")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				st := d.Status()
				if !st.LoggedIn || st.Server != fd.addr() || st.LastError != "" {
					t.Fatalf("status %+v", st)
				}
			}
			l := fd.logonList(t)
			if len(l) != tc.wantTries {
				t.Fatalf("got %d logons, want %d", len(l), tc.wantTries)
			}
			want := logon{callsign: "N0CALL-L", password: "secret", status: "ONLINE", location: "Example City"}
			if l[0] != want {
				t.Fatalf("logon %+v, want %+v", l[0], want)
			}
		})
	}
}

func TestLoginConfigErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"no callsign", Config{Password: "x"}},
		{"no password", Config{Callsign: "N0CALL"}},
		{"bad callsign", Config{Callsign: "N0 CALL", Password: "x"}},
		{"blank servers", Config{Callsign: "N0CALL", Password: "x", Servers: []string{" "}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Login(context.Background(), tc.cfg); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestRefreshAndLogoff(t *testing.T) {
	fd := newFakeDirectory(t, testList)
	clk := newFakeClock()
	d, err := Login(context.Background(), testConfig(clk, fd.addr()))
	if err != nil {
		t.Fatal(err)
	}
	clk.step(t, 5*time.Minute)
	fd.waitLogons(t, 2)
	clk.step(t, 5*time.Minute)
	fd.waitLogons(t, 3)

	// A failed renewal retries after Retry instead of Refresh.
	fd.mu.Lock()
	fd.reply = "BAD\n"
	fd.mu.Unlock()
	clk.step(t, 5*time.Minute)
	fd.waitLogons(t, 4)
	deadline := time.Now().Add(10 * time.Second)
	for d.Status().LoggedIn {
		if time.Now().After(deadline) {
			t.Fatal("status still logged in after a refused renewal")
		}
		time.Sleep(time.Millisecond)
	}
	if d.Status().LastError == "" {
		t.Fatal("no error recorded")
	}
	fd.mu.Lock()
	fd.reply = "OK\n"
	fd.mu.Unlock()
	clk.step(t, 30*time.Second)
	fd.waitLogons(t, 5)

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	l := fd.waitLogons(t, 6)
	if got := l[len(l)-1].status; got != "OFF-V" {
		t.Fatalf("last logon status %q, want OFF-V", got)
	}
	if _, err := d.Stations(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Stations after Close: %v", err)
	}
}

func TestLookup(t *testing.T) {
	fd := newFakeDirectory(t, testList)
	clk := newFakeClock()
	d, err := Login(context.Background(), testConfig(clk, fd.addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	tests := []struct {
		target    string
		wantCall  string
		wantLists int // cumulative list downloads after the lookup
	}{
		{"N1CALL", "N1CALL", 1},
		{"1998", "*EXAMPLE*", 1}, // cached
		{"example", "*EXAMPLE*", 1},
		{"N1CALL-R", "", 2}, // not in the cached list: one fresh download
	}
	for _, tc := range tests {
		st, err := d.Lookup(ctx, tc.target)
		if tc.wantCall == "" {
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("%s: err = %v", tc.target, err)
			}
		} else if err != nil || st.Callsign != tc.wantCall {
			t.Fatalf("%s: got %+v, %v", tc.target, st, err)
		}
		fd.mu.Lock()
		lists := fd.lists
		fd.mu.Unlock()
		if lists != tc.wantLists {
			t.Fatalf("%s: %d list downloads, want %d", tc.target, lists, tc.wantLists)
		}
	}
	// The cache expires after ListMaxAge.
	clk.Advance(DefaultListMaxAge)
	if _, err := d.Stations(ctx); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	lists := fd.lists
	fd.mu.Unlock()
	if lists != 3 {
		t.Fatalf("%d list downloads after expiry, want 3", lists)
	}
	if st := d.Status(); st.Stations != 3 {
		t.Fatalf("status stations %d", st.Stations)
	}
}
