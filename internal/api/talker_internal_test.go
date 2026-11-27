package api

import (
	"testing"

	"github.com/w1jar/nodespot/internal/nowcalling"
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

func TestPublicCallPlace(t *testing.T) {
	c := callIn(nowcalling.Call{Kind: "bm", Call: "N1CALL", City: "Exampleton", State: "ON", Country: "Canada", Start: "2026-11-27T12:00:00Z"})
	if c.Place != "Exampleton, ON, Canada" {
		t.Fatalf("non-US place: %q", c.Place)
	}
	c = callIn(nowcalling.Call{Kind: "bm", Call: "N0CALL", City: "Springfield", State: "ST", Country: "United States"})
	if c.Place != "Springfield, ST" {
		t.Fatalf("US place: %q", c.Place)
	}
	c = callIn(nowcalling.Call{Kind: "allstar", Call: "N0CALL", Location: "Home, ST"})
	if c.Place != "Home, ST" {
		t.Fatalf("location fallback: %q", c.Place)
	}
}
