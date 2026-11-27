package mode

import "testing"

func TestSearchEntries(t *testing.T) {
	list := []DirEntry{
		{Node: 2001, Callsign: "N1CALL-R", Location: "Shelbyville, XX"},
		{Node: 1997, Callsign: "N0CALL-L", Location: "Springfield, ST"},
		{Node: 9999, Callsign: "*ECHOTEST*", Location: "Test server"},
		{Node: 2002, Callsign: "*TEST*", Location: "Test conference"},
		{Node: 199, Callsign: "N2CALL", Location: "Nowhere"},
	}
	calls := func(l []DirEntry) (s []string) {
		for _, e := range l {
			s = append(s, e.Callsign)
		}
		return s
	}
	for _, c := range []struct {
		q     string
		limit int
		want  []string
	}{
		{"", 10, nil},
		{"**", 10, nil},
		{"n0", 10, []string{"N0CALL-L"}},
		{"CALL", 10, []string{"N0CALL-L", "N1CALL-R", "N2CALL"}},
		{"199", 10, []string{"N2CALL", "N0CALL-L"}}, // the exact node first, then the prefix
		{"20", 10, []string{"*TEST*", "N1CALL-R"}},
		{"test", 10, []string{"*TEST*", "*ECHOTEST*"}},
		{"*test*", 1, []string{"*TEST*"}},
		{"springfield", 10, []string{"N0CALL-L"}},
		{"sp", 10, nil}, // a location needs three characters
		{"n", 0, nil},
	} {
		got := calls(SearchEntries(list, c.q, c.limit))
		if len(got) != len(c.want) {
			t.Errorf("%q: got %v, want %v", c.q, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q: got %v, want %v", c.q, got, c.want)
				break
			}
		}
	}
	if r := SearchEntries(nil, "x", 5); r == nil {
		t.Error("no match must be an empty list, not nil")
	}
}
