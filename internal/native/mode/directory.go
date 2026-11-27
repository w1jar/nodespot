package mode

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DirEntry is one station of a mode's station directory (EchoLink), for the
// UI's target search.
type DirEntry struct {
	Node     int    `json:"node"`     // node number
	Callsign string `json:"callsign"` // upper case, e.g. N0CALL-L or *EXAMPLE*
	Location string `json:"location,omitempty"`
	// Status is "online", "busy" or the directory's own word in lower
	// case; "" when the entry gives none.
	Status string `json:"status,omitempty"`
	// Kind is "user", "link" (-L), "repeater" (-R) or "conference".
	Kind string `json:"kind"`
}

// DirResult answers a directory search. Loaded is false while the mode has
// no directory to search (EchoLink before its login); Results is then empty
// and Error may say why.
type DirResult struct {
	Loaded  bool       `json:"loaded"`
	Updated time.Time  `json:"updated,omitzero"` // when the list was downloaded
	Error   string     `json:"error,omitempty"`
	Results []DirEntry `json:"results"`
}

// Searcher is implemented by a mode with a station directory.
type Searcher interface {
	SearchDirectory(ctx context.Context, q string, limit int) (DirResult, error)
}

// SearchEntries returns at most limit entries of list matching q, best
// first: an exact callsign or node number, then callsigns starting with q,
// node numbers starting with q, callsigns containing q and, for three or
// more characters, locations containing q. Case and the asterisks of a
// conference name are ignored. An empty q matches nothing.
func SearchEntries(list []DirEntry, q string, limit int) []DirEntry {
	out := []DirEntry{}
	if limit <= 0 {
		return out
	}
	t := strings.Trim(strings.ToUpper(strings.TrimSpace(q)), "*")
	if t == "" {
		return out
	}
	type hit struct {
		rank int
		e    DirEntry
	}
	var hits []hit
	digits := t != "" && strings.Trim(t, "0123456789") == ""
	for _, e := range list {
		call := strings.Trim(e.Callsign, "*")
		node := strconv.Itoa(e.Node)
		rank := -1
		switch {
		case call == t || (digits && node == t):
			rank = 0
		case strings.HasPrefix(call, t):
			rank = 1
		case digits && strings.HasPrefix(node, t):
			rank = 2
		case strings.Contains(call, t):
			rank = 3
		case len(t) >= 3 && strings.Contains(strings.ToUpper(e.Location), t):
			rank = 4
		}
		if rank >= 0 {
			hits = append(hits, hit{rank, e})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		return hits[i].e.Callsign < hits[j].e.Callsign
	})
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].e)
	}
	return out
}
