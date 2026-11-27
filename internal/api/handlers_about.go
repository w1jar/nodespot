package api

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/w1jar/nodespot/internal/config"
	"github.com/w1jar/nodespot/internal/diag"
	"github.com/w1jar/nodespot/internal/store"
)

// AboutJSON is the API object of GET /api/about.
type AboutJSON struct {
	Version string `json:"version"`
	// NodeRunning says whether the node daemon answers; NodeStarted is when
	// it started (zero while it is not running).
	NodeRunning bool          `json:"nodeRunning"`
	NodeStarted time.Time     `json:"nodeStarted"`
	Platform    diag.Platform `json:"platform"`
}

// AddSecret registers a literal that diagnostics removes wherever it
// occurs.
func (s *Server) AddSecret(v string) {
	if v == "" {
		return
	}
	s.secretsMu.Lock()
	s.secrets = append(s.secrets, v)
	s.secretsMu.Unlock()
}

func (s *Server) knownSecrets() []string {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()
	return append([]string(nil), s.secrets...)
}

func (s *Server) platform() diag.Platform {
	s.platOnce.Do(func() {
		p := diag.DefaultPlatformPaths
		if s.d.PlatformPaths != nil {
			p = *s.d.PlatformPaths
		}
		s.plat = diag.ReadPlatform(p)
	})
	return s.plat
}

func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request, _ store.User) {
	st, running := s.nw.status()
	out := AboutJSON{Version: s.d.Version, NodeRunning: running, Platform: s.platform()}
	if running {
		out.NodeStarted = st.Started
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request, u store.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	rep := s.buildReport(ctx)
	s.d.Logger.Printf("diagnostics downloaded by %s", u.Username)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="nodespot-diagnostics-%s.txt"`, rep.Now.Format("2006-01-02")))
	_, _ = w.Write([]byte(rep.Render()))
}

func (s *Server) buildReport(ctx context.Context) diag.Report {
	rep := diag.Report{Now: time.Now(), Version: s.d.Version, Platform: s.platform(), Secrets: s.knownSecrets()}
	if s.d.System != nil {
		sys := s.d.System.Collect(ctx)
		rep.Uptime, rep.Load, rep.CPUTemp = sys.Uptime, sys.Load, sys.CPUTemp
		rep.MemTotal, rep.MemAvailable = sys.MemTotal, sys.MemAvailable
	}
	rep.Node = s.nodeReport(ctx)
	if s.d.Nodelist != nil {
		ns := s.d.Nodelist.Status()
		rep.NodelistCount, rep.NodelistSource, rep.NodelistUpdated = ns.Count, ns.Source, ns.Updated
		if ns.Error != nil {
			rep.NodelistError = *ns.Error
		}
	}
	if s.d.Updates != nil {
		in := s.d.Updates.Info(ctx)
		rep.UpdatesAvailable, rep.UpdatesInstalled, rep.UpdatesCandidate = in.Available, in.Installed, in.Candidate
		rep.UpdatesJob, rep.UpdatesJobLog = in.Job, in.JobLog
		if in.Reason != nil {
			rep.UpdatesReason = *in.Reason
		}
		if in.Checked != nil {
			rep.UpdatesChecked = *in.Checked
		}
	} else {
		rep.UpdatesReason = "not installed from a package"
	}
	if s.d.Firewall != nil {
		if fw, err := s.d.Firewall(ctx); err != nil {
			rep.HelperErr = err.Error()
		} else {
			rep.HaveFirewall, rep.FirewallPresent, rep.FirewallRunning, rep.FirewallEnabled = true, fw.Present, fw.Running, fw.Open
		}
	}
	rep.Config = configLines(s.d.Config, s.d.ConfigPath)
	if s.d.Log != nil {
		rep.Log = s.d.Log.Lines(200)
	}
	return rep
}

// configLines lists the active lines of the config file (secret values
// masked) followed by the effective configuration.
func configLines(c config.Config, path string) []string {
	var out []string
	if path != "" {
		if f, err := os.Open(path); err == nil {
			out = append(out, "# "+path+" (active lines)")
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				t := strings.TrimSpace(sc.Text())
				if t == "" || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "#") {
					continue
				}
				out = append(out, diag.RedactConfigLine(sc.Text()))
			}
			f.Close()
		} else {
			out = append(out, "# "+path+": not readable")
		}
	}
	out = append(out, "# effective",
		"[server]", "listen = "+c.Listen, "datadir = "+c.DataDir,
		"[node]", "socket = "+c.NodeSocket, "config = "+c.NodeConfig,
		"[nodelist]", "url = "+c.NodelistURL, "refresh = "+c.NodelistRefresh.String(),
		"[helper]", "socket = "+c.HelperSocket,
		"[updates]", "url = "+c.UpdatesURL)
	return out
}

// nodeReport is the node section of the diagnostics.
func (s *Server) nodeReport(ctx context.Context) diag.NodeState {
	ns := diag.NodeState{Socket: s.d.Config.NodeSocket}
	if s.d.Node == nil {
		ns.Error = errNoNode.Error()
		return ns
	}
	st, err := s.d.Node.Status(ctx)
	if err != nil {
		ns.Error = err.Error()
		return ns
	}
	ns.Running, ns.Callsign, ns.Started, ns.Configured = true, st.Callsign, st.Started, st.Configured
	ns.RadioPresent, ns.Active = st.Radio.Present, st.Active
	if m := st.Mode; m != nil {
		ns.State, ns.Detail, ns.ModeErr = string(m.State), m.Detail, m.Error
		if m.Target != nil {
			ns.Target = m.Target.ID
		}
	}
	if l, err := s.d.Node.Modes(ctx); err == nil {
		for _, m := range l {
			if m.Configured {
				ns.Modes = append(ns.Modes, m.Name)
			}
		}
	}
	return ns
}
