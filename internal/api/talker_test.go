package api_test

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/api"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/nowcalling"
	"github.com/w1jar/nodespot/internal/radioid"
)

// fakeRadioID is a radioid.net list on disk with two placeholder users.
func fakeRadioID(t *testing.T) *radioid.Manager {
	t.Helper()
	dir := t.TempDir()
	csv := radioid.Header + "\n" +
		"1234567,N0CALL,Pat,Example,Springfield,ST,United States\n" +
		"1234568,N1CALL,Sam,Example,Exampleton,ON,Canada\n"
	if err := os.WriteFile(filepath.Join(dir, radioid.FileName), []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	g := radioid.New(radioid.Options{Dir: dir, Logger: log.New(io.Discard, "", 0)})
	g.Load()
	if _, ok := g.ByID(1234568); !ok {
		t.Fatal("the fake list did not load")
	}
	return g
}

func talkerOf(n api.NodeJSON) *mode.Talker {
	if n.Mode == nil {
		return nil
	}
	return n.Mode.Talker
}

func TestTalkerLookup(t *testing.T) {
	e := newEnv(t, opts{radioid: fakeRadioID(t)})
	if c := e.do("op", "POST", "/api/select", map[string]string{"mode": "bm", "target": "91"}, nil); c != 200 {
		t.Fatalf("select bm: %d", c)
	}
	f := e.fake(mode.BM)

	// Only the DMR ID: the callsign, name and place come from the list.
	f.KeyTalker(mode.Talker{ID: 1234568, Target: "91"})
	n := e.waitNode("the talker", func(n api.NodeJSON) bool { return talkerOf(n) != nil && talkerOf(n).ID == 1234568 })
	if tk := talkerOf(n); tk.Callsign != "N1CALL" || tk.Name != "Sam Example" || tk.Place != "Exampleton, ON, Canada" {
		t.Fatalf("resolved talker: %+v", tk)
	}
	// What the network sent is kept.
	f.KeyTalker(mode.Talker{ID: 1234567, Callsign: "N0CALL", Name: "Radio Name", Target: "91"})
	n = e.waitNode("the second talker", func(n api.NodeJSON) bool { return talkerOf(n) != nil && talkerOf(n).ID == 1234567 })
	if tk := talkerOf(n); tk.Name != "Radio Name" || tk.Place != "Springfield, ST" {
		t.Fatalf("partly sent talker: %+v", tk)
	}
	// An unknown ID stays as it is (the UI shows "ID …").
	f.KeyTalker(mode.Talker{ID: 7654321, Target: "91"})
	n = e.waitNode("the unknown talker", func(n api.NodeJSON) bool { return talkerOf(n) != nil && talkerOf(n).ID == 7654321 })
	if tk := talkerOf(n); tk.Callsign != "" || tk.Name != "" || tk.Place != "" {
		t.Fatalf("unknown ID: %+v", tk)
	}
	time.Sleep(nowcalling.MinDuration + 50*time.Millisecond)
	f.Unkey()

	// The last-heard list: the ID-only call has the person filled in.
	var l []mode.Heard
	deadline := time.Now().Add(5 * time.Second)
	var row *mode.Heard
	for row == nil && time.Now().Before(deadline) {
		e.do("viewer", "GET", "/api/heard?limit=10", nil, &l)
		for i := range l {
			if l[i].ID == 1234568 {
				row = &l[i]
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if row == nil || row.Callsign != "N1CALL" || row.Name != "Sam Example" || row.Place != "Exampleton, ON, Canada" {
		t.Fatalf("heard row: %+v (%+v)", row, l)
	}
	for _, h := range l {
		if h.ID == 7654321 && (h.Callsign != "" || h.Name != "") {
			t.Fatalf("unknown heard row: %+v", h)
		}
	}

	// AllStar is never looked up, even for a callsign the list knows.
	if c := e.do("op", "POST", "/api/select", map[string]string{"mode": "allstar", "target": "2000"}, nil); c != 200 {
		t.Fatalf("select allstar: %d", c)
	}
	e.fake(mode.AllStar).Key("N1CALL")
	n = e.waitNode("the AllStar talker", func(n api.NodeJSON) bool { return n.Active == mode.AllStar && talkerOf(n) != nil })
	if tk := talkerOf(n); tk.Callsign != "N1CALL" || tk.Name != "" || tk.Place != "" {
		t.Fatalf("AllStar talker: %+v", tk)
	}
	time.Sleep(nowcalling.MinDuration + 50*time.Millisecond)
	e.fake(mode.AllStar).Unkey()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.do("viewer", "GET", "/api/heard?limit=10", nil, &l)
		if len(l) > 0 && l[0].Mode == mode.AllStar && !l[0].End.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(l) == 0 || l[0].Mode != mode.AllStar || l[0].Name != "" || l[0].Place != "" {
		t.Fatalf("AllStar heard row: %+v", l)
	}
}
