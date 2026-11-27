package api

import (
	"io"
	"log"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/nodelist"
)

func TestPlaceText(t *testing.T) {
	for _, tt := range []struct{ city, state, country, want string }{
		{"Springfield", "ST", "United States", "Springfield, ST"},
		{"Exampleton", "ON", "Canada", "Exampleton, ON, Canada"},
		{"Sampleburg", "", "Germany", "Sampleburg, Germany"},
		{"", "", "Japan", "Japan"},
		{"", "", "", ""},
	} {
		if got := placeText(tt.city, tt.state, tt.country); got != tt.want {
			t.Errorf("placeText(%q, %q, %q) = %q, want %q", tt.city, tt.state, tt.country, got, tt.want)
		}
	}
}

func TestEnrichAllStarNode(t *testing.T) {
	nl := nodelist.New(nodelist.Options{Logger: log.New(io.Discard, "", 0)})
	nl.Set([]nodelist.Info{{Node: "1997", Callsign: "N1CALL Hub", Description: "Example Hub", Location: "Springfield, ST"}}, "test", time.Now())
	s := &Server{d: Deps{Nodelist: nl}}

	l := []mode.Heard{
		{Mode: mode.AllStar, Callsign: "1997", Target: "1997", Place: "x"},
		{Mode: mode.AllStar, Callsign: "2002", Target: "1997"},
		{Mode: mode.AllStar, Callsign: "N0CALL", Target: "1997"},
	}
	s.enrichHeard(l)
	if g := l[0]; g.Callsign != "N1CALL" || g.Name != "Example Hub" || g.Place != "" || g.Target != "1997" {
		t.Fatalf("listed node: %+v", g)
	}
	if l[1].Callsign != "2002" || l[1].Name != "" {
		t.Fatalf("unlisted node changed: %+v", l[1])
	}
	if l[2].Callsign != "N0CALL" || l[2].Name != "" {
		t.Fatalf("callsign changed: %+v", l[2])
	}

	tk := &mode.Talker{Callsign: "1997", Target: "1997"}
	got := s.enrichTalker(mode.AllStar, tk)
	if got.Callsign != "N1CALL" || got.Name != "Example Hub" || got.Place != "" || tk.Callsign != "1997" {
		t.Fatalf("talker %+v (input %+v)", got, tk)
	}
	if s.enrichTalker(mode.AllStar, &mode.Talker{Callsign: "2002"}).Callsign != "2002" {
		t.Fatal("unlisted talker changed")
	}
}
