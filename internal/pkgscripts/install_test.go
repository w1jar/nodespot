package pkgscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func installScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "packaging", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// installBlocks returns the admin-setup and port-takeover blocks of install.sh.
func installBlocks(t *testing.T) string {
	t.Helper()
	s := installScript(t)
	var out string
	for _, name := range []string{"admin-setup", "port-takeover"} {
		i := strings.Index(s, "# ---8<--- "+name)
		j := strings.Index(s, "# --->8--- "+name)
		if i < 0 || j < i {
			t.Fatalf("%s markers not found", name)
		}
		out += s[i:j] + "\n"
	}
	return out
}

// tkEnv is a temporary root with a fake systemctl, ss and no firewall-cmd.
type tkEnv struct {
	dir  string
	bin  string
	conf string
}

func newTkEnv(t *testing.T, units map[string]string, listen string) *tkEnv {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	e := &tkEnv{dir: dir, bin: filepath.Join(dir, "bin"), conf: filepath.Join(dir, "nodespot.conf")}
	for _, d := range []string{e.bin, filepath.Join(dir, "sd"), filepath.Join(dir, "state"), filepath.Join(dir, "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// units: name -> "enabled|disabled|static" + ":" + "active|inactive"
	var list strings.Builder
	for u, st := range units {
		list.WriteString(u + " enabled enabled\n")
		p := strings.SplitN(st, ":", 2)
		e.write("fake/en-"+u, p[0])
		e.write("fake/ac-"+u, p[1])
	}
	e.write("fake/units", list.String())
	e.write("nodespot.conf", "[server]\nlisten = "+listen+"\n")
	fake := `#!/bin/sh
d=` + dir + `/fake
echo "$*" >>$d/calls
case "$1" in
list-unit-files) cat $d/units ;;
is-enabled) cat "$d/en-$2" 2>/dev/null || { echo not-found; exit 1; } ;;
is-active) cat "$d/ac-$2" 2>/dev/null || { echo inactive; exit 3; } ;;
disable) shift; [ "$1" = --now ] && shift; [ ! -f $d/fail-$1 ] || exit 1; echo disabled >"$d/en-$1"; echo inactive >"$d/ac-$1" ;;
enable) shift; [ "$1" = --now ] && shift; [ ! -f $d/fail-$1 ] || exit 1; echo enabled >"$d/en-$1"; echo active >"$d/ac-$1" ;;
stop) echo inactive >"$d/ac-$2" ;;
start) echo active >"$d/ac-$2" ;;
*) : ;;
esac
`
	e.writeExec("bin/systemctl", fake)
	e.writeExec("bin/ss", "#!/bin/sh\ncat "+dir+"/fake/ss 2>/dev/null\nexit 0\n")
	return e
}

func (e *tkEnv) write(rel, content string) {
	p := filepath.Join(e.dir, rel)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		panic(err)
	}
}

func (e *tkEnv) writeExec(rel, content string) {
	e.write(rel, content)
	if err := os.Chmod(filepath.Join(e.dir, rel), 0o755); err != nil {
		panic(err)
	}
}

func (e *tkEnv) read(rel string) string {
	b, _ := os.ReadFile(filepath.Join(e.dir, rel))
	return string(b)
}

func (e *tkEnv) run(t *testing.T, call string) (string, error) {
	t.Helper()
	script := `say() { printf '%s\n' "nodespot-install: $*"; }
die() { printf '%s\n' "nodespot-install: error: $*" >&2; exit 1; }
tmp="$NODESPOT_T"
PORT_WANT="${PORT_WANT:-}"
` + installBlocks(t) + "\n" + call + "\n"
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{
		"PATH=" + e.bin + ":" + os.Getenv("PATH"),
		"NODESPOT_T=" + filepath.Join(e.dir, "tmp"),
		"NODESPOT_CONF=" + e.conf,
		"NODESPOT_SYSTEMD_DIR=" + filepath.Join(e.dir, "sd"),
		"NODESPOT_DROPIN_DIR=" + filepath.Join(e.dir, "dropin"),
		"NODESPOT_WEBPORT_STATE=" + filepath.Join(e.dir, "state"),
		"NODESPOT_TAKEOVER_LIST=" + filepath.Join(e.dir, "lib", "takeover.list"),
		"NODESPOT_DISABLED_UNITS=" + filepath.Join(e.dir, "lib", "disabled-units"),
		"PORT_WANT=" + os.Getenv("PORT_WANT_TEST"),
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestTakeoverDisableSelectsUnits(t *testing.T) {
	e := newTkEnv(t, map[string]string{
		"apache2.service":  "enabled:active",
		"allmon3.service":  "enabled:active",
		"nginx.service":    "disabled:inactive", // not enabled, not running: left alone
		"lighttpd.service": "disabled:active",   // running only: stopped
		"asterisk.service": "enabled:active",    // never touched
		"cockpit.service":  "enabled:active",    // never touched
	}, ":8080")
	out, err := e.run(t, "takeover_disable")
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, want := range []string{"disabled and stopped: allmon3.service", "disabled and stopped: apache2.service", "stopped (it was running but not enabled): lighttpd.service"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	list := e.read("lib/takeover.list")
	if list != "allmon3.service enabled\napache2.service enabled\nlighttpd.service active\n" {
		t.Errorf("takeover.list = %q", list)
	}
	if rec := e.read("lib/disabled-units"); rec != "allmon3\napache2\n" {
		t.Errorf("disabled-units = %q", rec)
	}
	calls := e.read("fake/calls")
	for _, bad := range []string{"asterisk", "cockpit", "nginx.service", "nodespot"} {
		for _, l := range strings.Split(calls, "\n") {
			if (strings.HasPrefix(l, "disable") || strings.HasPrefix(l, "stop") || strings.HasPrefix(l, "enable") || strings.HasPrefix(l, "start")) && strings.Contains(l, bad) {
				t.Errorf("touched %s: %q", bad, l)
			}
		}
	}
	// Running again changes nothing and keeps the record.
	if out, err = e.run(t, "takeover_disable"); err != nil || !strings.Contains(out, "nothing was turned off") {
		t.Fatalf("second run: %v %s", err, out)
	}
	if e.read("lib/takeover.list") != list {
		t.Errorf("list changed by a second run: %q", e.read("lib/takeover.list"))
	}
}

func TestTakeoverNoUnits(t *testing.T) {
	e := newTkEnv(t, map[string]string{"asterisk.service": "enabled:active"}, ":8080")
	out, err := e.run(t, "takeover_disable")
	if err != nil || !strings.Contains(out, "nothing was turned off") {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "lib", "takeover.list")); err == nil {
		t.Error("list written although nothing was turned off")
	}
}

func TestSetPort80WritesDropinAndUndoRestores(t *testing.T) {
	e := newTkEnv(t, map[string]string{"apache2.service": "enabled:active"}, "192.0.2.10:8080")
	if out, err := e.run(t, "takeover_disable; set_port 80"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got := e.read("nodespot.conf"); !strings.Contains(got, "listen = 192.0.2.10:80\n") {
		t.Errorf("conf = %q", got)
	}
	d := e.read("dropin/port80.conf")
	for _, w := range []string{"[Service]\n", "AmbientCapabilities=CAP_NET_BIND_SERVICE\n", "CapabilityBoundingSet=CAP_NET_BIND_SERVICE\n"} {
		if !strings.Contains(d, w) {
			t.Errorf("drop-in lacks %q: %s", w, d)
		}
	}
	if p := e.read("state/previous-listen"); p != "192.0.2.10:8080\n" {
		t.Errorf("previous-listen = %q", p)
	}
	calls := e.read("fake/calls")
	if !strings.Contains(calls, "daemon-reload") || !strings.Contains(calls, "restart nodespot") {
		t.Errorf("calls: %s", calls)
	}
	if strings.Index(calls, "daemon-reload") > strings.Index(calls, "restart nodespot") {
		t.Errorf("restart before daemon-reload: %s", calls)
	}
	// Undo.
	out, err := e.run(t, "PORT_WANT=; takeover_undo")
	if err != nil {
		t.Fatalf("undo: %v: %s", err, out)
	}
	if got := e.read("nodespot.conf"); !strings.Contains(got, "listen = 192.0.2.10:8080\n") {
		t.Errorf("conf after undo = %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "dropin", "port80.conf")); err == nil {
		t.Error("drop-in still there after undo")
	}
	if e.read("fake/en-apache2.service") != "enabled\n" || e.read("fake/ac-apache2.service") != "active\n" {
		t.Error("apache2 not enabled and started again")
	}
	if _, err := os.Stat(filepath.Join(e.dir, "lib", "takeover.list")); err == nil {
		t.Error("takeover.list kept after a full undo")
	}
	if rec := e.read("lib/disabled-units"); rec != "" {
		t.Errorf("disabled-units after undo = %q", rec)
	}
	// Undo again: nothing to do.
	if out, err = e.run(t, "takeover_undo"); err != nil || !strings.Contains(out, "nothing to undo") {
		t.Fatalf("second undo: %v %s", err, out)
	}
}

func TestSetPort80RefusedWhenBusy(t *testing.T) {
	e := newTkEnv(t, nil, ":8080")
	e.write("fake/ss", "LISTEN 0 511 *:80 *:* users:((\"lighttpd\",pid=612,fd=4))\n")
	out, err := e.run(t, "set_port 80")
	if err == nil {
		t.Fatalf("expected a failing status: %s", out)
	}
	if !strings.Contains(out, `"lighttpd"`) || !strings.Contains(out, "NOT moved") || !strings.Contains(out, "pid=612") {
		t.Errorf("message: %s", out)
	}
	if got := e.read("nodespot.conf"); !strings.Contains(got, "listen = :8080\n") {
		t.Errorf("conf changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "dropin", "port80.conf")); err == nil {
		t.Error("drop-in written although the move was refused")
	}
}

func TestSetPortHighKeepsNoDropin(t *testing.T) {
	e := newTkEnv(t, nil, ":8080")
	if out, err := e.run(t, "set_port 8181"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "dropin")); err == nil {
		t.Error("drop-in directory created for a high port")
	}
	if got := e.read("nodespot.conf"); !strings.Contains(got, "listen = :8181\n") {
		t.Errorf("conf = %q", got)
	}
}

func TestUndoIgnoresUnknownEntries(t *testing.T) {
	e := newTkEnv(t, map[string]string{"apache2.service": "disabled:inactive"}, ":8080")
	e.write("lib/takeover.list", "asterisk.service enabled\nsshd.service enabled\napache2.service enabled\n")
	out, err := e.run(t, "takeover_undo")
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	calls := e.read("fake/calls")
	if strings.Contains(calls, "asterisk") || strings.Contains(calls, "sshd") {
		t.Errorf("unknown units were touched: %s", calls)
	}
	if !strings.Contains(calls, "enable --now apache2.service") {
		t.Errorf("apache2 not enabled: %s", calls)
	}
}

func TestUndoKeepsFailedUnits(t *testing.T) {
	e := newTkEnv(t, map[string]string{"apache2.service": "disabled:inactive", "allmon3.service": "disabled:inactive"}, ":8080")
	e.write("lib/takeover.list", "allmon3.service enabled\napache2.service enabled\n")
	e.write("fake/fail-apache2.service", "x")
	_, err := e.run(t, "takeover_undo")
	if err == nil {
		t.Fatal("expected failure status")
	}
	if got := e.read("lib/takeover.list"); got != "apache2.service enabled\n" {
		t.Errorf("list = %q", got)
	}
}

// installHead runs the option parsing of install.sh (up to the architecture
// check) with the given environment and reports the result.
func installHead(t *testing.T, env ...string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	s := installScript(t)
	i := strings.Index(s, "# --- takeover (NODESPOT_TAKEOVER)")
	j := strings.Index(s, "# --- architecture")
	if i < 0 || j < i {
		t.Fatal("option parsing not found")
	}
	script := `die() { printf '%s\n' "error: $*" >&2; exit 1; }
` + s[i:j] + `printf 'port=%s takeover=%s undo=%s dv=%s vocoder=%s\n' "$PORT_WANT" "$TAKEOVER_WANT" "$TAKEOVER_UNDO" "$DV_WANT" "$VOCODER_WANT"
`
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestInstallOptionParsing(t *testing.T) {
	ok := []struct {
		env  []string
		want string
	}{
		{nil, "port= takeover=0 undo=0 dv=0 vocoder=0"},
		{[]string{"NODESPOT_TAKEOVER=1"}, "port=80 takeover=1 undo=0 dv=0 vocoder=0"},
		{[]string{"NODESPOT_TAKEOVER=yes"}, "port=80 takeover=1 undo=0 dv=0 vocoder=0"},
		{[]string{"NODESPOT_TAKEOVER=TRUE"}, "port=80 takeover=1 undo=0 dv=0 vocoder=0"},
		{[]string{"NODESPOT_TAKEOVER=1", "NODESPOT_PORT=8181"}, "port=8181 takeover=1 undo=0 dv=0 vocoder=0"},
		{[]string{"NODESPOT_TAKEOVER=1", "NODESPOT_DV=1"}, "port=80 takeover=1 undo=0 dv=1 vocoder=0"},
		{[]string{"NODESPOT_TAKEOVER=undo"}, "port= takeover=0 undo=1 dv=0 vocoder=0"},
		{[]string{"NODESPOT_TAKEOVER=undo", "NODESPOT_PORT=8181"}, "port=8181 takeover=0 undo=1 dv=0 vocoder=0"},
		{[]string{"NODESPOT_PORT=80"}, "port=80 takeover=0 undo=0 dv=0 vocoder=0"},
		{[]string{"NODESPOT_PORT=1024"}, "port=1024 takeover=0 undo=0 dv=0 vocoder=0"},
		{[]string{"NODESPOT_VOCODER=1"}, "port= takeover=0 undo=0 dv=0 vocoder=1"},
		{[]string{"NODESPOT_VOCODER=Yes", "NODESPOT_TAKEOVER=1"}, "port=80 takeover=1 undo=0 dv=0 vocoder=1"},
	}
	for _, c := range ok {
		got, err := installHead(t, c.env...)
		if err != nil || got != c.want {
			t.Errorf("%v: got %q (%v), want %q", c.env, got, err, c.want)
		}
	}
	bad := [][]string{
		{"NODESPOT_TAKEOVER=0"},
		{"NODESPOT_TAKEOVER=maybe"},
		{"NODESPOT_TAKEOVER=undo", "NODESPOT_PORT=80"},
		{"NODESPOT_TAKEOVER=undo", "NODESPOT_DV=1"},
		{"NODESPOT_VOCODER=0"},
		{"NODESPOT_VOCODER=maybe"},
		{"NODESPOT_TAKEOVER=undo", "NODESPOT_VOCODER=1"},
		{"NODESPOT_PORT=443"},
		{"NODESPOT_PORT=81"},
		{"NODESPOT_PORT=080"},
		{"NODESPOT_PORT=0"},
		{"NODESPOT_PORT=70000"},
		{"NODESPOT_PORT=abc"},
	}
	for _, env := range bad {
		if got, err := installHead(t, env...); err == nil {
			t.Errorf("%v accepted: %q", env, got)
		}
	}
}

// archCheck runs check_arch of install.sh with a fake dpkg (reporting arch)
// and uname (reporting kernel). The fake dpkg logs every call, so a test can
// see that nothing but --print-architecture was run.
func archCheck(t *testing.T, arch, kernel string) (string, string, error) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	s := installScript(t)
	i := strings.Index(s, "# ---8<--- arch-check")
	j := strings.Index(s, "# --->8--- arch-check")
	if i < 0 || j < i {
		t.Fatal("arch-check markers not found")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	fake := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake("dpkg", "echo \"dpkg $*\" >> "+calls+"\n[ \"$1\" = --print-architecture ] && echo "+arch+" && exit 0\nexit 1\n")
	fake("uname", "echo "+kernel+"\n")
	script := `die() { printf '%s\n' "error: $*" >&2; exit 1; }
` + s[i:j] + "check_arch\necho ok\n"
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	log, _ := os.ReadFile(calls)
	return strings.TrimSpace(string(out)), string(log), err
}

func TestInstallArchAccepted(t *testing.T) {
	for _, c := range [][2]string{{"arm64", "aarch64"}, {"amd64", "x86_64"}} {
		out, _, err := archCheck(t, c[0], c[1])
		if err != nil || out != "ok" {
			t.Errorf("%s: %q (%v)", c[0], out, err)
		}
	}
}

func TestInstallArchAcceptsArmhfWithNote(t *testing.T) {
	for _, kernel := range []string{"armv6l", "armv7l", "aarch64"} { // 32-bit userland, 32- or 64-bit kernel
		out, log, err := archCheck(t, "armhf", kernel)
		if err != nil {
			t.Fatalf("armhf on %s refused: %q (%v)", kernel, out, err)
		}
		for _, want := range []string{"32-bit", "hotspot", "not supported on 32-bit boards", "software AMBE vocoder", "ok"} {
			if !strings.Contains(out, want) {
				t.Errorf("armhf output lacks %q: %q", want, out)
			}
		}
		if log != "dpkg --print-architecture\n" {
			t.Errorf("dpkg calls = %q", log)
		}
	}
}

func TestInstallArchRefusesArmel(t *testing.T) {
	out, _, err := archCheck(t, "armel", "armv5tel")
	if err == nil {
		t.Fatalf("armel accepted: %q", out)
	}
	if !strings.Contains(out, "armel") || !strings.Contains(out, "Nothing was changed") {
		t.Errorf("message = %q", out)
	}
}

func TestInstallArchRefusesOthers(t *testing.T) {
	if _, _, err := archCheck(t, "i386", "i686"); err == nil {
		t.Error("i386 accepted")
	}
}

// The check runs before the pre-flight, the keyring and the apt source, so a
// refused system is left untouched.
func TestInstallArchCheckBeforeChanges(t *testing.T) {
	s := installScript(t)
	call := strings.Index(s, "check_arch\n\n# --- pre-flight")
	if call < 0 {
		t.Fatal("check_arch call not found before the pre-flight")
	}
	var code []string // the lines before the call, without comments
	for _, l := range strings.Split(s[:call], "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			code = append(code, l)
		}
	}
	before := strings.Join(code, "\n")
	for _, later := range []string{"install -m 0644 \"$tmp/keyring.gpg\"", "apt-get "} {
		if strings.Contains(before, later) {
			t.Errorf("%q comes before the architecture check", later)
		}
	}
}

// TestInstallVocoderFailureOnlyWarns runs install_vocoder with a fake helper:
// success is reported, and a failing or missing helper only warns (the
// function returns 0, so the installer goes on and exits 0).
func TestInstallVocoderFailureOnlyWarns(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	s := installScript(t)
	i := strings.Index(s, "VOCODER_RESULT=\n")
	j := strings.Index(s, "if [ \"$TAKEOVER_UNDO\" -eq 1 ]; then\n\trc=0")
	if i < 0 || j < i {
		t.Fatal("install_vocoder not found")
	}
	dir := t.TempDir()
	mk := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct{ helper, want string }{
		{mk("ok", `[ "$*" = "vocoder-install --wait" ] || exit 9; echo working`), "RESULT=installed and running"},
		{mk("bad", `echo "vocoder-install: Debian bullseye is not supported" >&2; exit 1`), "RESULT=not installed (warning"},
		{filepath.Join(dir, "missing"), "RESULT=not installed (warning"},
	}
	for _, c := range cases {
		script := `say() { printf '%s\n' "nodespot-install: $*"; }
` + s[i:j] + `rc=0
install_vocoder || rc=$?
printf 'rc=%s RESULT=%s\n' "$rc" "$VOCODER_RESULT"
`
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "NODESPOT_HELPER=" + c.helper}
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "rc=0 "+c.want) {
			t.Errorf("%s: %v\n%s", filepath.Base(c.helper), err, out)
		}
		if strings.Contains(c.want, "not installed") && !strings.Contains(string(out), "Install it later from the Modes page") {
			t.Errorf("%s: no warning:\n%s", filepath.Base(c.helper), out)
		}
	}
}
