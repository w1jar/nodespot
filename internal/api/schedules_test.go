package api_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/api"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/schedules"
)

func ny(t *testing.T) *time.Location {
	t.Helper()
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func schedBody(over map[string]any) map[string]any {
	b := map[string]any{"name": "Tuesday net", "enabled": true, "mode": "allstar", "target": "1999", "action": "connect",
		"days": []int{2}, "date": "", "start": "19:00", "end": "19:05"}
	for k, v := range over {
		b[k] = v
	}
	return b
}

func (e *env) snapshot(role string) schedules.Snapshot {
	e.t.Helper()
	var s schedules.Snapshot
	if c := e.do(role, "GET", "/api/schedules", nil, &s); c != 200 {
		e.t.Fatalf("GET /api/schedules: %d", c)
	}
	return s
}

func TestSchedulesUnavailable(t *testing.T) {
	e := newEnv(t, opts{})
	expectErr(t, e, "viewer", "GET", "/api/schedules", nil, 503, "schedules_unavailable")
	expectErr(t, e, "op", "POST", "/api/schedules", schedBody(nil), 503, "schedules_unavailable")
	expectErr(t, e, "op", "PUT", "/api/schedules/enabled", map[string]any{"enabled": false}, 503, "schedules_unavailable")
	// manual actions work without the feature
	if c := e.do("op", "POST", "/api/select", map[string]string{"mode": "allstar", "target": "2000"}, nil); c != 200 {
		t.Fatal(c)
	}
}

func TestSchedulesCRUDAndValidation(t *testing.T) {
	e := newEnv(t, opts{schedules: true})

	snap := e.snapshot("viewer")
	if !snap.Enabled || len(snap.Schedules) != 0 || snap.Timezone != "America/New_York" || len(snap.Upcoming) != 0 {
		t.Fatalf("empty snapshot: %+v", snap)
	}

	var created schedules.Schedule
	if c := e.do("op", "POST", "/api/schedules", schedBody(nil), &created); c != 201 {
		t.Fatalf("create: %d", c)
	}
	if created.ID == 0 || created.Name != "Tuesday net" || !created.Enabled || created.Target != "1999" ||
		created.Start != "19:00" || created.End != "19:05" || len(created.Days) != 1 || created.Days[0] != 2 || created.LastRun != nil {
		t.Fatalf("created: %+v", created)
	}
	path := "/api/schedules/" + itoa(created.ID)

	snap = e.snapshot("viewer")
	if len(snap.Schedules) != 1 || len(snap.Upcoming) != 4 || snap.Upcoming[0].Action != "connect" ||
		snap.Upcoming[0].At.Format(time.RFC3339) != "2026-11-17T19:00:00-05:00" {
		t.Fatalf("snapshot: %+v", snap)
	}

	// full replace: a one-off disconnect of any BM talkgroup
	var upd schedules.Schedule
	if c := e.do("op", "PUT", path, schedBody(map[string]any{"action": "disconnect", "end": "", "days": []int{}, "date": "2026-11-18",
		"mode": "BM", "target": "", "enabled": false, "name": " Evening "}), &upd); c != 200 {
		t.Fatalf("update: %d", c)
	}
	if upd.ID != created.ID || upd.Action != "disconnect" || upd.Date != "2026-11-18" || len(upd.Days) != 0 || upd.Mode != "bm" || upd.Target != "" ||
		upd.Enabled || upd.Name != "Evening" {
		t.Fatalf("updated: %+v", upd)
	}
	expectErr(t, e, "op", "PUT", "/api/schedules/9999", schedBody(nil), 404, "not_found")

	// validation
	bad := []map[string]any{
		{"target": ""},
		{"target": "a\nb"},
		{"days": []int{}, "date": ""},
		{"days": []int{1}, "date": "2026-12-01"},
		{"days": []int{7}},
		{"days": []int{}, "date": "2026-02-30"},
		{"action": "disconnect", "end": "20:00"},
		{"action": "toggle"},
		{"end": "19:00"},
		{"start": "7:00"},
		{"start": "24:00"},
		{"end": "19:60"},
		{"mode": ""},
		{"name": strings.Repeat("x", 61)},
	}
	for _, b := range bad {
		expectErr(t, e, "op", "POST", "/api/schedules", schedBody(b), 400, "bad_request")
		expectErr(t, e, "op", "PUT", path, schedBody(b), 400, "bad_request")
	}
	expectErr(t, e, "op", "POST", "/api/schedules", schedBody(map[string]any{"mode": "nosuchmode"}), 400, "unknown_mode")
	expectErr(t, e, "op", "PUT", "/api/schedules/abc", schedBody(nil), 400, "bad_request")
	if got := e.snapshot("op").Schedules; len(got) != 1 {
		t.Fatalf("invalid bodies were stored: %+v", got)
	}

	// midnight-crossing end is valid
	if c := e.do("op", "POST", "/api/schedules", schedBody(map[string]any{"start": "23:00", "end": "01:00"}), nil); c != 201 {
		t.Fatal(c)
	}

	if c := e.do("op", "DELETE", path, nil, nil); c != 204 {
		t.Fatalf("delete: %d", c)
	}
	expectErr(t, e, "op", "DELETE", path, nil, 404, "not_found")
	if got := e.snapshot("op").Schedules; len(got) != 1 {
		t.Fatalf("after delete: %+v", got)
	}
}

func TestSchedulesRoles(t *testing.T) {
	e := newEnv(t, opts{schedules: true})
	var s schedules.Schedule
	if c := e.do("admin", "POST", "/api/schedules", schedBody(nil), &s); c != 201 {
		t.Fatal(c)
	}
	path := "/api/schedules/" + itoa(s.ID)
	for _, role := range []string{"viewer", "op", "admin"} {
		if e.do(role, "GET", "/api/schedules", nil, nil) != 200 {
			t.Fatalf("%s cannot read", role)
		}
	}
	expectErr(t, e, "viewer", "POST", "/api/schedules", schedBody(nil), 403, "forbidden")
	expectErr(t, e, "viewer", "PUT", path, schedBody(nil), 403, "forbidden")
	expectErr(t, e, "viewer", "DELETE", path, nil, 403, "forbidden")
	expectErr(t, e, "viewer", "PUT", "/api/schedules/enabled", map[string]any{"enabled": false}, 403, "forbidden")
	expectErr(t, e, "", "GET", "/api/schedules", nil, 401, "unauthorized")
	if !e.snapshot("viewer").Enabled {
		t.Fatal("viewer changed the switch")
	}
}

func TestSchedulesEnabledSwitch(t *testing.T) {
	e := newEnv(t, opts{schedules: true})
	var snap schedules.Snapshot
	if c := e.do("op", "PUT", "/api/schedules/enabled", map[string]any{"enabled": false}, &snap); c != 200 || snap.Enabled {
		t.Fatalf("%d %+v", c, snap)
	}
	if e.snapshot("viewer").Enabled {
		t.Fatal("not persisted")
	}
	if v, _ := e.st.GetSetting("schedules.enabled"); v != "false" {
		t.Fatalf("setting %q", v)
	}
	if c := e.do("op", "PUT", "/api/schedules/enabled", map[string]any{"enabled": true}, &snap); c != 200 || !snap.Enabled {
		t.Fatalf("%d %+v", c, snap)
	}
	expectErr(t, e, "op", "PUT", "/api/schedules/enabled", map[string]any{}, 400, "bad_request")
}

func TestManualActionsPauseSchedules(t *testing.T) {
	e := newEnv(t, opts{schedules: true})
	cases := []struct {
		name, path string
		body       any
		reason     string
	}{
		{"select", "/api/select", map[string]any{"mode": "allstar", "target": "2000"}, "select by op"},
		{"connect", "/api/connect", map[string]any{"target": "1998"}, "connect by op"},
		{"disconnect", "/api/disconnect", nil, "disconnect by op"},
	}
	for _, c := range cases {
		e.do("op", "PUT", "/api/schedules/enabled", map[string]any{"enabled": true}, nil)
		if !e.snapshot("op").Enabled {
			t.Fatal("not enabled")
		}
		if code := e.do("op", "POST", c.path, c.body, nil); code != 200 {
			t.Fatalf("%s: %d", c.name, code)
		}
		s := e.snapshot("viewer")
		if s.Enabled || s.PausedReason != c.reason || s.PausedAt == nil {
			t.Fatalf("%s: %+v", c.name, s)
		}
	}
	// a rejected command does not pause
	e.do("op", "PUT", "/api/schedules/enabled", map[string]any{"enabled": true}, nil)
	expectErr(t, e, "op", "POST", "/api/select", map[string]any{"mode": "allstar", "target": "a\nb"}, 400, "bad_target")
	expectErr(t, e, "op", "POST", "/api/select", map[string]any{"mode": "nosuchmode"}, 409, "node_refused")
	expectErr(t, e, "viewer", "POST", "/api/disconnect", nil, 403, "forbidden")
	if !e.snapshot("op").Enabled {
		t.Fatal("paused by a rejected command")
	}
}

func TestSchedulesEndToEnd(t *testing.T) {
	e := newEnv(t, opts{schedules: true})
	l := ny(t)
	st := e.stream("viewer")
	defer st.cancel()

	var s schedules.Schedule
	if c := e.do("op", "POST", "/api/schedules", schedBody(nil), &s); c != 201 { // Tuesdays 19:00-19:05
		t.Fatal(c)
	}
	st.wait(func(ev sseEvent) bool {
		return ev.name == "schedules" && strings.Contains(ev.data, `"schedules":[{"id":`)
	})

	as := e.fake(mode.AllStar)
	run := func(at time.Time) []mode.Target {
		e.clk.set(at)
		before := len(as.Connects())
		e.eng.RunOnce(context.Background())
		return as.Connects()[before:]
	}

	if got := run(time.Date(2026, 11, 17, 18, 59, 55, 0, l)); len(got) != 0 {
		t.Fatalf("early: %v", got)
	}
	got := run(time.Date(2026, 11, 17, 19, 0, 5, 0, l))
	if len(got) != 1 || got[0].ID != "1999" {
		t.Fatalf("start: %v", got)
	}
	snap := e.snapshot("viewer")
	if !snap.Enabled || snap.PausedReason != "" {
		t.Fatalf("the scheduler's own command paused the schedules: %+v", snap)
	}
	if len(snap.Active) != 1 || snap.Active[0].ID != s.ID || snap.Schedules[0].LastResult != "ok" || snap.Schedules[0].LastRun == nil {
		t.Fatalf("snapshot after start: %+v", snap)
	}
	st.wait(func(ev sseEvent) bool {
		return ev.name == "schedules" && strings.Contains(ev.data, `"lastResult":"ok"`)
	})
	e.waitNode("linked", func(n api.NodeJSON) bool {
		return n.Active == mode.AllStar && n.Mode != nil && n.Mode.State == mode.StateConnected
	})
	_, _, d0, _ := as.Counts()
	run(time.Date(2026, 11, 17, 19, 5, 5, 0, l))
	if _, _, d, _ := as.Counts(); d != d0+1 {
		t.Fatalf("end: %d disconnects, want %d", d, d0+1)
	}
	if s := e.snapshot("op"); !s.Enabled || len(s.Active) != 0 {
		t.Fatalf("after end: %+v", s)
	}
}

func TestSchedulesSwitchMode(t *testing.T) {
	e := newEnv(t, opts{schedules: true})
	l := ny(t)
	e.do("op", "POST", "/api/select", map[string]string{"mode": "allstar", "target": "2000"}, nil)
	e.do("op", "PUT", "/api/schedules/enabled", map[string]any{"enabled": true}, nil)
	if c := e.do("op", "POST", "/api/schedules", schedBody(map[string]any{"mode": "bm", "target": "91", "end": ""}), nil); c != 201 {
		t.Fatal(c)
	}
	e.clk.set(time.Date(2026, 11, 17, 19, 0, 5, 0, l))
	e.eng.RunOnce(context.Background())
	if c := e.fake(mode.BM).Connects(); len(c) != 1 || c[0].ID != "91" {
		t.Fatalf("bm: %+v", c)
	}
	if e.fake(mode.AllStar).Running() {
		t.Fatal("AllStar still runs")
	}
	// a disconnect of another mode leaves the active one alone
	if c := e.do("op", "POST", "/api/schedules", schedBody(map[string]any{"name": "Off", "action": "disconnect", "end": "", "start": "19:10"}), nil); c != 201 {
		t.Fatal(c)
	}
	e.waitNode("bm", func(n api.NodeJSON) bool { return n.Active == mode.BM })
	e.clk.set(time.Date(2026, 11, 17, 19, 10, 5, 0, l))
	e.eng.RunOnce(context.Background())
	if _, _, d, _ := e.fake(mode.BM).Counts(); d != 0 {
		t.Fatalf("an AllStar disconnect dropped BM (%d)", d)
	}
}

func TestSchedulesAlreadyLinkedIsSkipped(t *testing.T) {
	e := newEnv(t, opts{schedules: true})
	l := ny(t)
	e.do("op", "POST", "/api/select", map[string]string{"mode": "allstar", "target": "1999"}, nil)
	e.do("op", "PUT", "/api/schedules/enabled", map[string]any{"enabled": true}, nil)
	if c := e.do("op", "POST", "/api/schedules", schedBody(map[string]any{"end": ""}), nil); c != 201 {
		t.Fatal(c)
	}
	e.waitNode("linked", func(n api.NodeJSON) bool { return n.Mode != nil && n.Mode.State == mode.StateConnected })
	as := e.fake(mode.AllStar)
	before := len(as.Connects())
	e.clk.set(time.Date(2026, 11, 17, 19, 0, 5, 0, l))
	e.eng.RunOnce(context.Background())
	if got := as.Connects()[before:]; len(got) != 0 {
		t.Fatalf("connects: %v", got)
	}
	if r := e.snapshot("op").Schedules[0].LastResult; r != "skipped: already connected" {
		t.Fatalf("result %q", r)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestSchedulesOverlapRefused(t *testing.T) {
	e := newEnv(t, opts{schedules: true})
	var a schedules.Schedule
	if c := e.do("op", "POST", "/api/schedules", schedBody(map[string]any{"start": "19:00", "end": "20:30"}), &a); c != 201 {
		t.Fatal(c)
	}
	// one radio: overlapping windows clash whatever their modes
	var got struct{ Error, Code string }
	body := schedBody(map[string]any{"name": "Net", "mode": "bm", "target": "91", "start": "20:00", "end": "22:00"})
	if c := e.do("op", "POST", "/api/schedules", body, &got); c != 409 || got.Code != "schedule_conflict" ||
		!strings.Contains(got.Error, `"Tuesday net" (Tue 19:00–20:30)`) {
		t.Fatalf("%d %+v", c, got)
	}
	// another day is fine
	var b schedules.Schedule
	body["days"] = []int{3}
	if c := e.do("op", "POST", "/api/schedules", body, &b); c != 201 {
		t.Fatalf("other day: %d", c)
	}
	// moving it onto Tuesday via PUT is refused
	body["days"] = []int{2}
	body["name"] = ""
	expectErr(t, e, "op", "PUT", "/api/schedules/"+itoa(b.ID), body, 409, "schedule_conflict")
	// a disabled schedule never conflicts; enabling it (a full PUT) does
	body["enabled"] = false
	if c := e.do("op", "PUT", "/api/schedules/"+itoa(b.ID), body, nil); c != 200 {
		t.Fatalf("disabled: %d", c)
	}
	body["enabled"] = true
	expectErr(t, e, "op", "PUT", "/api/schedules/"+itoa(b.ID), body, 409, "schedule_conflict")
	// saving a schedule over itself does not conflict with itself
	if c := e.do("op", "PUT", "/api/schedules/"+itoa(a.ID), schedBody(map[string]any{"start": "19:00", "end": "20:45"}), nil); c != 200 {
		t.Fatalf("self update: %d", c)
	}
	// a disconnect is never a conflict
	if c := e.do("op", "POST", "/api/schedules", schedBody(map[string]any{"action": "disconnect", "end": "", "start": "19:10"}), nil); c != 201 {
		t.Fatalf("disconnect: %d", c)
	}
}

func TestSchedulesSettingsEndpoint(t *testing.T) {
	e := newEnv(t, opts{schedules: true})
	if s := e.snapshot("viewer"); s.ManualAction != "pause" || s.ResumeAt != nil || s.LastCancel != nil {
		t.Fatalf("%+v", s)
	}
	var snap schedules.Snapshot
	if c := e.do("op", "PUT", "/api/schedules/settings", map[string]any{"manualAction": "cancel"}, &snap); c != 200 || snap.ManualAction != "cancel" {
		t.Fatalf("%d %+v", c, snap)
	}
	if v, _ := e.st.GetSetting("schedules.manual_action"); v != "cancel" {
		t.Fatalf("setting %q", v)
	}
	if c := e.do("admin", "PUT", "/api/schedules/settings", map[string]any{"manualAction": "pause_today"}, &snap); c != 200 || snap.ManualAction != "pause_today" {
		t.Fatalf("%d %+v", c, snap)
	}
	expectErr(t, e, "op", "PUT", "/api/schedules/settings", map[string]any{"manualAction": "nope"}, 400, "bad_request")
	expectErr(t, e, "op", "PUT", "/api/schedules/settings", map[string]any{}, 400, "bad_request")
	expectErr(t, e, "viewer", "PUT", "/api/schedules/settings", map[string]any{"manualAction": "pause"}, 403, "forbidden")
	if e.snapshot("viewer").ManualAction != "pause_today" {
		t.Fatal("rejected request changed the setting")
	}
	off := newEnv(t, opts{})
	expectErr(t, off, "op", "PUT", "/api/schedules/settings", map[string]any{"manualAction": "pause"}, 503, "schedules_unavailable")
}

func TestManualActionModesThroughAPI(t *testing.T) {
	l := ny(t)
	// pause_today: resumes at the next local midnight
	e := newEnv(t, opts{schedules: true})
	e.do("op", "PUT", "/api/schedules/settings", map[string]any{"manualAction": "pause_today"}, nil)
	if c := e.do("op", "POST", "/api/select", map[string]any{"mode": "allstar", "target": "2000"}, nil); c != 200 {
		t.Fatal(c)
	}
	s := e.snapshot("viewer")
	if s.Enabled || s.ResumeAt == nil || !s.ResumeAt.Equal(time.Date(2026, 11, 18, 0, 0, 0, 0, l)) {
		t.Fatalf("%+v", s)
	}

	// cancel: the running window ends, schedules stay on, notice recorded
	e = newEnv(t, opts{schedules: true})
	e.do("op", "PUT", "/api/schedules/settings", map[string]any{"manualAction": "cancel"}, nil)
	var a schedules.Schedule
	if c := e.do("op", "POST", "/api/schedules", schedBody(map[string]any{"start": "19:00", "end": "20:30"}), &a); c != 201 {
		t.Fatal(c)
	}
	e.clk.set(time.Date(2026, 11, 17, 19, 0, 5, 0, l))
	e.eng.RunOnce(context.Background())
	if len(e.snapshot("op").Active) != 1 {
		t.Fatal("window not running")
	}
	e.clk.set(time.Date(2026, 11, 17, 19, 10, 0, 0, l))
	if c := e.do("op", "POST", "/api/connect", map[string]any{"target": "2000"}, nil); c != 200 {
		t.Fatal(c)
	}
	s = e.snapshot("viewer")
	if !s.Enabled || len(s.Active) != 0 || s.LastCancel == nil || s.LastCancel.Reason != "connect by op" ||
		len(s.LastCancel.Names) != 1 || s.LastCancel.Names[0] != "Tuesday net" {
		t.Fatalf("%+v", s)
	}
}
