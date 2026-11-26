package helper

import "strings"

// confLine is one line of an nodespot.conf: its section, and for a key line
// the lower-case key and where the value sits in the line.
type confLine struct {
	text           string
	section, key   string
	header         bool
	valFrom, valTo int
}

// cutComment returns the length of line before a comment: a ";" or "#" at
// the start or after a blank (as config.Load reads it).
func cutComment(line string) int {
	for i := 0; i < len(line); i++ {
		if (line[i] == ';' || line[i] == '#') && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return i
		}
	}
	return len(line)
}

func parseConfLines(data string) []confLine {
	sec := ""
	var out []confLine
	for _, text := range strings.Split(data, "\n") {
		l := confLine{text: text}
		body := text[:cutComment(text)]
		t := strings.TrimSpace(body)
		switch {
		case strings.HasPrefix(t, "["):
			if end := strings.IndexByte(t, ']'); end > 0 {
				sec = strings.ToLower(strings.TrimSpace(t[1:end]))
				l.header = true
			}
		case strings.Contains(t, "="):
			eq := strings.IndexByte(body, '=')
			l.key = strings.ToLower(strings.TrimSpace(body[:eq]))
			from := eq + 1
			for from < len(body) && (body[from] == ' ' || body[from] == '\t') {
				from++
			}
			l.valFrom, l.valTo = from, len(strings.TrimRight(body, " \t\r"))
			if l.valTo < from {
				l.valTo = from
			}
		}
		l.section = sec
		out = append(out, l)
	}
	return out
}

// confListen returns the effective [server] listen value of an nodespot.conf
// (the last one, as config.Load reads it) and whether there is one.
func confListen(data []byte) (string, bool) {
	v, _, ok := listenLine(parseConfLines(string(data)))
	return v, ok
}

// listenLine returns the last [server] listen value, its line and whether
// there is one.
func listenLine(lines []confLine) (string, int, bool) {
	v, at, ok := "", -1, false
	for i, l := range lines {
		if l.section == "server" && l.key == "listen" {
			if t := l.text[l.valFrom:l.valTo]; t != "" {
				v, at, ok = t, i, true
			}
		}
	}
	return v, at, ok
}

// keepListen makes the restored nodespot.conf keep this node's web port: the
// [server] listen of backupConf is replaced by the one in currentConf (or
// added to [server] when the backup has none). Everything else in the
// backup file is kept byte for byte. When currentConf has no listen line,
// backupConf is returned unchanged. kept is the value taken from the node,
// backupListen the backup's own value ("" when it had none) and changed
// reports whether out differs from backupConf.
func keepListen(backupConf, currentConf []byte) (out []byte, kept, backupListen string, changed bool) {
	cur, ok := confListen(currentConf)
	if !ok {
		return backupConf, "", "", false
	}
	lines := parseConfLines(string(backupConf))
	was, at, has := listenLine(lines)
	if has && was == cur {
		return backupConf, cur, was, false
	}
	text := make([]string, len(lines))
	for i, l := range lines {
		text[i] = l.text
	}
	switch {
	case has:
		l := lines[at]
		text[at] = l.text[:l.valFrom] + cur + l.text[l.valTo:]
	default:
		// After the last key (or the header) of the first [server].
		ins := -1
		for i, l := range lines {
			if l.section != "server" {
				if ins >= 0 {
					break
				}
				continue
			}
			if l.header && ins < 0 || l.key != "" {
				ins = i
			}
		}
		if ins >= 0 {
			text = append(text[:ins+1], append([]string{"listen = " + cur}, text[ins+1:]...)...)
		} else {
			s := strings.Join(text, "\n")
			if s != "" && !strings.HasSuffix(s, "\n") {
				s += "\n"
			}
			if s != "" {
				s += "\n"
			}
			s += "[server]\nlisten = " + cur + "\n"
			return []byte(s), cur, "", true
		}
	}
	res := strings.Join(text, "\n")
	return []byte(res), cur, was, res != string(backupConf)
}
