package helper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/w1jar/nodespot/internal/backup"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/node"
)

// maxRestoreFile bounds each node or config file read from an archive.
const maxRestoreFile = 4 << 20

// restoreSet is the files of an archive the helper restores (the database
// is the server's).
type restoreSet struct {
	m     *backup.Manifest
	files map[string][]byte
}

func readRestoreSet(in io.Reader) (*restoreSet, error) {
	rs := &restoreSet{files: map[string][]byte{}}
	m, err := backup.Read(in, func(e backup.Entry, body io.Reader) error {
		switch e.Path {
		case backup.PathNodeConfig, backup.PathSA818, backup.PathNodespotConf:
		default:
			return nil
		}
		if e.Size > maxRestoreFile {
			return &backup.Error{Msg: e.Path + " is too large to restore"}
		}
		var b bytes.Buffer
		if _, err := io.Copy(&b, body); err != nil {
			return err
		}
		rs.files[e.Path] = b.Bytes()
		return nil
	})
	if err != nil {
		return nil, err
	}
	rs.m = m
	return rs, nil
}

// mergeSecrets fills every blank secret of the restored config from the
// config on this node, then leaves out the modes that still lack a required
// setting (a backup made without passwords, restored on a node that has
// none). It returns the titles of the modes left out.
func mergeSecrets(restored, current *node.Config) []string {
	var missing []string
	names := make([]string, 0, len(restored.Modes))
	for n := range restored.Modes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		s := restored.Modes[name]
		sp, ok := mode.Lookup(name)
		if !ok {
			continue // Validate reports it
		}
		if s.Values == nil {
			s.Values = map[string]string{}
		}
		var cur mode.Settings
		if current != nil {
			cur = current.Modes[name]
		}
		blank := false
		for _, f := range sp.Fields {
			if !f.Secret || strings.TrimSpace(s.Values[f.Key]) != "" {
				continue
			}
			if v := cur.Values[f.Key]; v != "" {
				s.Values[f.Key] = v
			} else if f.Required {
				blank = true
			}
		}
		if len(s.Values) == 0 {
			s.Values = nil
		}
		restored.Modes[name] = s
		if blank && sp.CheckEnv(mode.Env{Callsign: restored.Station.Callsign, DMRID: restored.Station.DMRID, NXDNID: restored.Station.NXDNID}, s) != nil {
			delete(restored.Modes, name)
			missing = append(missing, sp.Title)
			if a := restored.Station.Active; a != nil && a.Mode == name {
				restored.Station.Active = nil
			}
		}
	}
	return missing
}

// planNode validates and merges the node config of the archive.
func (o *Ops) planNode(rs *restoreSet, res *RestoreResult) ([]byte, error) {
	raw, ok := rs.files[backup.PathNodeConfig]
	if !ok {
		return nil, nil
	}
	c, err := parseNodeConfig(raw)
	if err != nil {
		return nil, &backup.Error{Msg: err.Error()}
	}
	var cur *node.Config
	if b, _, err := readRegular(o.nodeConfig(), maxNodeFile); err == nil {
		cur, _ = parseNodeConfig(b)
	}
	res.MissingSecrets = append(res.MissingSecrets, mergeSecrets(c, cur)...)
	if err := c.Validate(); err != nil {
		return nil, &backup.Error{Msg: "the node config in the backup is not valid: " + err.Error()}
	}
	for n := range c.Modes {
		res.Modes = append(res.Modes, n)
	}
	sort.Strings(res.Modes)
	return encodeNodeConfig(c)
}

// BackupRestore validates the archive read from in completely, then (unless
// req.DryRun) writes the node config, the SA818 settings and nodespot.conf and
// restarts the node. Errors before the first change are returned as errors;
// failures while applying are reported in the result.
func (o *Ops) BackupRestore(ctx context.Context, req *RestoreRequest, in io.Reader) (RestoreResult, error) {
	if req == nil {
		req = &RestoreRequest{}
	}
	o.applyMu.Lock()
	defer o.applyMu.Unlock()

	rs, err := readRestoreSet(in)
	if err != nil {
		return RestoreResult{}, err
	}
	osi := o.osInfo()
	res := RestoreResult{DryRun: req.DryRun, Steps: []RestoreStep{}, Arch: o.arch(ctx), OSID: osi.ID,
		OSCodename: osi.Codename, OSName: osi.Pretty, Modes: []string{}, MissingSecrets: []string{}}
	if b, _, err := readRegular(o.nodeConfig(), maxNodeFile); err == nil {
		if c, err := parseNodeConfig(b); err == nil {
			res.Callsign = c.Station.Callsign
		}
	}
	nodeData, err := o.planNode(rs, &res)
	if err != nil {
		return RestoreResult{}, err
	}
	sa := rs.files[backup.PathSA818]
	if sa != nil {
		if err := validSA818(sa); err != nil {
			return RestoreResult{}, &backup.Error{Msg: err.Error()}
		}
	}
	conf := rs.files[backup.PathNodespotConf]
	if conf != nil && (!utf8.Valid(conf) || bytes.IndexByte(conf, 0) >= 0) {
		return RestoreResult{}, &backup.Error{Msg: "nodespot.conf in the backup is not a text file"}
	}
	if req.DryRun {
		res.OK = true
		return res, nil
	}

	step := func(name string, err error, detail string) bool {
		st := RestoreStep{Name: name, OK: err == nil, Detail: detail}
		if err != nil {
			st.Detail = err.Error()
			res.Error = name + ": " + err.Error()
		}
		res.Steps = append(res.Steps, st)
		return err == nil
	}
	nuid, ngid := lookupIDs(o.nodeUser(), o.nodeUser(), "root")
	if nodeData != nil {
		detail := "modes: " + strings.Join(res.Modes, ", ")
		if len(res.Modes) == 0 {
			detail = "no modes set up"
		}
		if len(res.MissingSecrets) > 0 {
			detail += "; set up again (no password in the backup): " + strings.Join(res.MissingSecrets, ", ")
		}
		if !step("Restore node settings", writeFileAt(o.nodeConfig(), nodeData, nuid, ngid, 0o600), detail) {
			return res, nil
		}
	}
	if sa != nil {
		if !step("Restore SA818 settings", writeFileAt(o.sa818State(), sa, nuid, ngid, 0o600), "") {
			return res, nil
		}
	}
	if conf != nil {
		detail, err := o.restoreNodespotConf(conf, rs.m.Find(backup.PathNodespotConf))
		if !step("Restore nodespot.conf", err, detail) {
			return res, nil
		}
	}
	if nodeData != nil || sa != nil {
		out, code, err := o.Run.Run(ctx, "systemctl", "restart", nodeService)
		if err == nil && code != 0 {
			err = fmt.Errorf("systemctl restart failed: %s", LastLines(out, 3))
		}
		if !step("Restart the node", err, "") {
			return res, nil
		}
	}
	res.OK = true
	return res, nil
}

// restoreNodespotConf writes the backup's nodespot.conf but keeps this node's web
// port (the [server] listen of the current file), so a backup from a node
// on another port does not move nodespot away from the port it was installed
// on and opened in the firewall. The detail names the kept port when the
// backup's differed.
func (o *Ops) restoreNodespotConf(data []byte, e *backup.Entry) (string, error) {
	detail := ""
	if cur, err := os.ReadFile(o.nodespotConf()); err == nil {
		out, kept, was, changed := keepListen(data, cur)
		if changed {
			data = out
			detail = "kept this node's web port (listen = " + kept + "); the backup had "
			if was == "" {
				detail += "no listen setting"
			} else {
				detail += was
			}
		}
	}
	uid, gid := lookupIDs("root", "nodespot", "root")
	perm := fs.FileMode(0o640)
	if e != nil {
		uid, gid = lookupIDs(e.Owner, e.Group, "root")
		if m, err := backup.ParseMode(e.Mode); err == nil {
			perm = fs.FileMode(m & 0o664)
		}
	}
	if err := writeFileAt(o.nodespotConf(), data, uid, gid, perm); err != nil {
		return "", errors.New("writing nodespot.conf: " + err.Error())
	}
	return detail, nil
}
