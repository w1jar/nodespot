package nowcalling

import (
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
)

type clk struct{ t time.Time }

func (c *clk) now() time.Time      { return c.t }
func (c *clk) add(d time.Duration) { c.t = c.t.Add(d) }

func newT() (*Tracker, *clk, *int) {
	c := &clk{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	tr := New()
	tr.Now = c.now
	n := 0
	tr.OnChange = func(Snapshot) { n++ }
	return tr, c, &n
}

func TestTalkStartEnd(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		talker  mode.Talker
		hold    time.Duration
		recent  int
		call    string
		node    string
		dmrID   string
		endMode string
	}{
		{"dmr call", mode.BM, mode.Talker{Callsign: "N0CALL", ID: 1234567, Target: "91"}, 3 * time.Second, 1, "N0CALL", "", "1234567", mode.BM},
		{"allstar node", mode.AllStar, mode.Talker{Callsign: "1999"}, 2 * time.Second, 1, "", "1999", "", mode.AllStar},
		{"glitch", mode.YSF, mode.Talker{Callsign: "N1CALL"}, 100 * time.Millisecond, 0, "N1CALL", "", "", mode.YSF},
		{"end any", mode.M17, mode.Talker{Callsign: "N1CALL"}, time.Second, 1, "N1CALL", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, c, _ := newT()
			tr.TalkStart(tt.mode, tt.talker)
			s := tr.Snapshot()
			if s.Current == nil || s.Current.Call != tt.call || s.Current.Node != tt.node || s.Current.DMRID != tt.dmrID || !s.Current.Active {
				t.Fatalf("current %+v", s.Current)
			}
			c.add(tt.hold)
			tr.TalkEnd(tt.endMode)
			s = tr.Snapshot()
			if s.Current != nil || len(s.Recent) != tt.recent {
				t.Fatalf("after end: %+v", s)
			}
		})
	}
}

func TestOtherModeEndIgnored(t *testing.T) {
	tr, c, _ := newT()
	tr.TalkStart(mode.YSF, mode.Talker{Callsign: "N0CALL"})
	c.add(time.Second)
	tr.TalkEnd(mode.BM)
	if tr.Snapshot().Current == nil {
		t.Fatal("an end on another mode ended the call")
	}
}

func TestNewTalkerReplaces(t *testing.T) {
	tr, c, _ := newT()
	tr.TalkStart(mode.BM, mode.Talker{Callsign: "N0CALL"})
	c.add(2 * time.Second)
	tr.TalkStart(mode.BM, mode.Talker{Callsign: "N1CALL"})
	s := tr.Snapshot()
	if s.Current.Call != "N1CALL" || len(s.Recent) != 1 || s.Recent[0].Call != "N0CALL" {
		t.Fatalf("%+v", s)
	}
}

func TestRecentBoundedAndSeed(t *testing.T) {
	tr, c, _ := newT()
	base := c.t
	var heard []mode.Heard
	for i := 0; i < RecentMax+5; i++ {
		st := base.Add(-time.Duration(i+1) * time.Minute)
		heard = append(heard, mode.Heard{Mode: mode.DMR, Callsign: "N0CALL", Start: st, End: st.Add(5 * time.Second)})
	}
	heard = append(heard, mode.Heard{Mode: mode.DMR, Callsign: "N1CALL", Start: base}) // ongoing: skipped
	tr.Seed(heard)
	tr.Seed(heard) // idempotent
	s := tr.Snapshot()
	if len(s.Recent) != RecentMax || s.Current != nil {
		t.Fatalf("recent %d", len(s.Recent))
	}
	for i := 1; i < len(s.Recent); i++ {
		if s.Recent[i].start.After(s.Recent[i-1].start) {
			t.Fatal("not newest first")
		}
	}
}

func TestResolveAndRefill(t *testing.T) {
	tr, c, n := newT()
	tr.Resolve = func(c *Call) { c.Desc = "resolved" }
	tr.TalkStart(mode.YSF, mode.Talker{Callsign: "N0CALL"})
	c.add(time.Second)
	tr.TalkEnd(mode.YSF)
	before := *n
	tr.Refill(func(c *Call) bool { c.Name = "Pat"; return true })
	s := tr.Snapshot()
	if s.Recent[0].Desc != "resolved" || s.Recent[0].Name != "Pat" || *n != before+1 {
		t.Fatalf("%+v %d", s.Recent[0], *n)
	}
	tr.Refill(func(c *Call) bool { return true }) // all named now
	if *n != before+1 {
		t.Fatal("refill without change emitted")
	}
}
