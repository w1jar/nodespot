package helper

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseTermHostArgs(t *testing.T) {
	id := "0123456789abcdef"
	sock := "/run/nodespot/term/" + id + ".sock"
	ok := []string{"--id=" + id, "--sock=" + sock, "--rows=30", "--cols=100", "--owner=7"}
	a, err := parseTermHostArgs(ok)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != id || a.Sock != sock || a.Rows != 30 || a.Cols != 100 || a.Owner != 7 {
		t.Fatalf("%+v", a)
	}
	// owner is optional
	if _, err := parseTermHostArgs(ok[:4]); err != nil {
		t.Fatal(err)
	}
	with := func(i int, v string) []string {
		c := append([]string(nil), ok...)
		c[i] = v
		return c
	}
	bad := map[string][]string{
		"short id":         with(0, "--id=0123"),
		"upper id":         with(0, "--id=0123456789ABCDEF"),
		"path id":          with(0, "--id=../../../etc/passwd"),
		"sock elsewhere":   with(1, "--sock=/tmp/"+id+".sock"),
		"sock traversal":   with(1, "--sock=/run/nodespot/term/../"+id+".sock"),
		"sock other id":    with(1, "--sock=/run/nodespot/term/ffffffffffffffff.sock"),
		"sock subdir":      with(1, "--sock=/run/nodespot/term/x/"+id+".sock"),
		"empty sock":       with(1, "--sock="),
		"zero rows":        with(2, "--rows=0"),
		"huge rows":        with(2, "--rows=501"),
		"zero cols":        with(3, "--cols=0"),
		"huge cols":        with(3, "--cols=99999999999"),
		"negative owner":   with(4, "--owner=-1"),
		"extra argument":   append(append([]string(nil), ok...), "login"),
		"unknown flag":     append(append([]string(nil), ok...), "--cmd=/bin/sh"),
		"missing size":     {"--id=" + id, "--sock=" + sock},
		"non numeric rows": with(2, "--rows=x"),
	}
	for name, args := range bad {
		if _, err := parseTermHostArgs(args); err == nil {
			t.Errorf("%s: accepted %v", name, args)
		}
	}
}

func TestRingBuffer(t *testing.T) {
	r := newRing(8)
	r.Write([]byte("abc"))
	r.Write([]byte("def"))
	if got := string(r.Bytes()); got != "abcdef" {
		t.Fatalf("%q", got)
	}
	r.Write([]byte("ghij")) // 10 bytes: the oldest two go
	if got := string(r.Bytes()); got != "cdefghij" {
		t.Fatalf("%q", got)
	}
	r.Write([]byte("0123456789")) // larger than the buffer: only the tail
	if got := string(r.Bytes()); got != "23456789" {
		t.Fatalf("%q", got)
	}
	// Bytes is a copy
	b := r.Bytes()
	b[0] = 'X'
	if string(r.Bytes()) != "23456789" {
		t.Fatal("Bytes shares memory")
	}
	// many small writes stay bounded and ordered
	r = newRing(termRingSize)
	var all bytes.Buffer
	for i := 0; i < 5000; i++ {
		s := strings.Repeat(string(rune('a'+i%26)), 37)
		r.Write([]byte(s))
		all.WriteString(s)
	}
	want := all.Bytes()[all.Len()-termRingSize:]
	if !bytes.Equal(r.Bytes(), want) {
		t.Fatal("ring content differs from the tail of the output")
	}
	if len(r.buf) > termRingSize || cap(r.buf) > 4*termRingSize {
		t.Fatalf("ring grew: len %d cap %d", len(r.buf), cap(r.buf))
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
}

func TestGraceNoApt(t *testing.T) {
	c := newFakeClock()
	apt := false
	g := newGraceMachine(2*time.Minute, 5*time.Second, 2*time.Hour, func() bool { return apt })
	// attached: never ends
	c.add(10 * time.Hour)
	if r := g.Tick(c.now()); r != "" {
		t.Fatalf("attached session ended: %s", r)
	}
	g.Detach(c.now())
	c.add(119 * time.Second)
	if r := g.Tick(c.now()); r != "" {
		t.Fatalf("ended inside the grace: %s", r)
	}
	// reattach inside the grace resets everything
	g.Attach()
	c.add(time.Hour)
	if r := g.Tick(c.now()); r != "" {
		t.Fatalf("reattached session ended: %s", r)
	}
	g.Detach(c.now())
	c.add(2 * time.Minute)
	if r := g.Tick(c.now()); r != EndGrace {
		t.Fatalf("got %q", r)
	}
}

func TestGraceWaitsForApt(t *testing.T) {
	c := newFakeClock()
	apt := true
	checks := 0
	g := newGraceMachine(2*time.Minute, 5*time.Second, 2*time.Hour, func() bool { checks++; return apt })
	g.Detach(c.now())
	c.add(119 * time.Second)
	g.Tick(c.now())
	if checks != 0 {
		t.Fatal("detector consulted inside the grace period")
	}
	c.add(2 * time.Second)
	if r := g.Tick(c.now()); r != "" {
		t.Fatalf("ended while apt runs: %s", r)
	}
	if checks != 1 {
		t.Fatalf("checks = %d", checks)
	}
	// polled at most every 5 seconds
	c.add(time.Second)
	g.Tick(c.now())
	if checks != 1 {
		t.Fatalf("polled too often: %d", checks)
	}
	for i := 0; i < 100; i++ {
		c.add(5 * time.Second)
		if r := g.Tick(c.now()); r != "" {
			t.Fatalf("ended while apt runs: %s", r)
		}
	}
	apt = false
	c.add(5 * time.Second)
	if r := g.Tick(c.now()); r != EndAptFinished {
		t.Fatalf("got %q", r)
	}
}

func TestGraceHardCap(t *testing.T) {
	c := newFakeClock()
	g := newGraceMachine(2*time.Minute, 5*time.Second, 2*time.Hour, func() bool { return true })
	g.Detach(c.now())
	c.add(2*time.Hour - time.Second)
	// walk up to just below the cap in poll steps
	if r := g.Tick(c.now()); r != "" {
		t.Fatalf("ended before the cap: %s", r)
	}
	c.add(time.Second)
	if r := g.Tick(c.now()); r != EndGrace {
		t.Fatalf("cap not enforced: %q", r)
	}
	// the cap counts from the detach, so a session that reattaches and
	// detaches again gets a fresh window
	g.Attach()
	g.Detach(c.now())
	c.add(3 * time.Hour / 2)
	if r := g.Tick(c.now()); r != "" {
		t.Fatalf("cap did not reset on detach: %q", r)
	}
}

func TestGraceAptThatStartsAfterGrace(t *testing.T) {
	// apt that only shows up at the first check after the grace still holds
	// the session; one that is gone by then does not.
	c := newFakeClock()
	apt := false
	g := newGraceMachine(time.Minute, time.Second, time.Hour, func() bool { return apt })
	g.Detach(c.now())
	c.add(time.Minute)
	if r := g.Tick(c.now()); r != EndGrace {
		t.Fatalf("got %q", r)
	}
}
