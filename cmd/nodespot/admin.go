package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/w1jar/nodespot/internal/auth"
	"github.com/w1jar/nodespot/internal/config"
	"github.com/w1jar/nodespot/internal/store"
)

// Exit codes of "nodespot admin".
const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitNoUsers  = 3 // "list --quiet" only: the database has no users
	defaultAdmin = "admin"
)

const adminUsage = `Usage: nodespot admin <command> [options]

Manage nodespot accounts directly in the database of the configured data
directory. Safe while the service is running. Run as root (sudo) or as the
service user, because the database is not world-writable.

Commands:
  create [--username NAME] [--role admin|operator|viewer]
        Create an account (default role: admin). Prompts for the username
        (default "admin") and, twice and without echo, for the password.
        Creating the first account completes the first-run setup.
  reset-password USER
        Set a new password for USER and sign that user out everywhere.
  setup-code
        Print the one-time first-run setup code (the web setup page takes it
        to create the administrator or restore a backup). Only while no
        account exists; otherwise exits 1. The setup link is
        http://<host>:<port>/#/setup?code=CODE
  list [--quiet]
        List accounts. With --quiet print nothing and exit 3 when there are
        no accounts (0 otherwise), for scripts.

Options (all commands):
  --config PATH   configuration file (default ` + config.DefaultConfigPath + `)

Input: on a terminal the prompts are interactive. When standard input is not a
terminal (scripting) the password and its confirmation are read as two lines
from standard input, and the username is --username or "admin".

Exit codes: 0 ok, 1 error, 2 usage, 3 "list --quiet" found no accounts.
`

// adminEnv bundles the process-level inputs so tests can drive runAdmin.
type adminEnv struct {
	in     io.Reader
	out    io.Writer
	errw   io.Writer
	isTerm func() bool // is stdin an interactive terminal?
	fd     int         // descriptor for term.ReadPassword
}

func adminMain(args []string) int {
	return runAdmin(args, adminEnv{
		in: os.Stdin, out: os.Stdout, errw: os.Stderr,
		isTerm: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		fd:     int(os.Stdin.Fd()),
	})
}

func runAdmin(args []string, env adminEnv) int {
	if len(args) == 0 {
		fmt.Fprint(env.errw, adminUsage)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(env.out, adminUsage)
		return exitOK
	case "create", "reset-password", "list", "setup-code":
	default:
		fmt.Fprintf(env.errw, "nodespot admin: unknown command %q\n\n%s", cmd, adminUsage)
		return exitUsage
	}

	fs := flag.NewFlagSet("nodespot admin "+cmd, flag.ContinueOnError)
	fs.SetOutput(env.errw)
	fs.Usage = func() { fmt.Fprint(env.errw, adminUsage) }
	cfgPath := fs.String("config", config.DefaultConfigPath, "path to nodespot.conf")
	var username, role string
	var quiet bool
	switch cmd {
	case "create":
		fs.StringVar(&username, "username", "", "account name (default: prompt, \"admin\")")
		fs.StringVar(&role, "role", store.RoleAdmin, "role")
	case "list":
		fs.BoolVar(&quiet, "quiet", false, "print nothing; exit 3 when there are no accounts")
		fs.BoolVar(&quiet, "q", false, "same as --quiet")
	}
	pos, err := parseInterspersed(fs, rest)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(env.out, adminUsage)
			return exitOK
		}
		return exitUsage
	}
	switch cmd {
	case "reset-password":
		if len(pos) != 1 {
			fmt.Fprint(env.errw, "nodespot admin reset-password: expected exactly one USER\n\n"+adminUsage)
			return exitUsage
		}
	default:
		if len(pos) != 0 {
			fmt.Fprintf(env.errw, "nodespot admin %s: unexpected argument %q\n\n%s", cmd, pos[0], adminUsage)
			return exitUsage
		}
	}
	if cmd == "create" && !store.ValidRole(role) {
		fmt.Fprintf(env.errw, "nodespot admin create: invalid role %q (admin, operator or viewer)\n", role)
		return exitUsage
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(env.errw, "nodespot admin: %v\n", err)
		return exitError
	}
	st, done, err := openAdminStore(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(env.errw, "nodespot admin: %v\n", err)
		return exitError
	}
	defer done()

	var rerr error
	code := exitOK
	switch cmd {
	case "create":
		rerr = adminCreate(st, cfg.DataDir, username, role, env)
	case "reset-password":
		rerr = adminResetPassword(st, pos[0], env)
	case "list":
		code, rerr = adminList(st, quiet, env)
	case "setup-code":
		code, rerr = adminSetupCode(st, cfg.DataDir, env)
	}
	if rerr != nil {
		fmt.Fprintf(env.errw, "nodespot admin %s: %v\n", cmd, rerr)
		return exitError
	}
	return code
}

// parseInterspersed parses flags that may appear before or after positionals.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

var adminDBNames = []string{"nodespot.db", "nodespot.db-wal", "nodespot.db-shm"}

// openAdminStore opens the store in dir. The returned func closes it and, when
// running as root on a directory owned by someone else (the service user),
// hands the database files back to that owner so the service can still write.
func openAdminStore(dir string) (*store.Store, func(), error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("data directory %s: %v (is nodespot installed? check --config)", dir, errors.Unwrap(err))
	}
	if !fi.IsDir() {
		return nil, nil, fmt.Errorf("data directory %s is not a directory", dir)
	}
	dbPath := filepath.Join(dir, "nodespot.db")
	if syscall.Access(dir, 2) != nil {
		return nil, nil, fmt.Errorf("cannot write to %s: run as root (sudo nodespot admin ...) or as the service user", dir)
	}
	if f, err := os.OpenFile(dbPath, os.O_RDWR, 0); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("cannot write to %s: run as root (sudo nodespot admin ...) or as the service user", dbPath)
		}
	} else {
		f.Close()
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("database: %w", err)
	}
	done := func() {
		_ = st.Close()
		fixOwnership(dir)
	}
	return st, done, nil
}

func fixOwnership(dir string) {
	if os.Geteuid() != 0 {
		return
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return
	}
	sys, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	for _, n := range adminDBNames {
		p := filepath.Join(dir, n)
		pfi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if psys, ok := pfi.Sys().(*syscall.Stat_t); ok && (psys.Uid != sys.Uid || psys.Gid != sys.Gid) {
			_ = os.Lchown(p, int(sys.Uid), int(sys.Gid))
		}
	}
}

// reader serves prompts either interactively or from stdin lines.
type reader struct {
	env  adminEnv
	term bool
	br   *bufio.Reader
}

func newReader(env adminEnv) *reader {
	return &reader{env: env, term: env.isTerm != nil && env.isTerm(), br: bufio.NewReader(env.in)}
}

func (r *reader) line() (string, error) {
	s, err := r.br.ReadString('\n')
	if err != nil && (err != io.EOF || s == "") {
		if err == io.EOF {
			return "", errors.New("unexpected end of input")
		}
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// password asks for a new password twice and validates it.
func (r *reader) password() (string, error) {
	var a, b string
	if r.term {
		var err error
		for i, p := range []*string{&a, &b} {
			prompt := "Password: "
			if i == 1 {
				prompt = "Confirm password: "
			}
			fmt.Fprint(r.env.out, prompt)
			var raw []byte
			raw, err = term.ReadPassword(r.env.fd)
			fmt.Fprintln(r.env.out)
			if err != nil {
				return "", err
			}
			*p = string(raw)
		}
	} else {
		var err error
		if a, err = r.line(); err != nil {
			return "", fmt.Errorf("reading password: %w", err)
		}
		if b, err = r.line(); err != nil {
			return "", fmt.Errorf("reading password confirmation: %w", err)
		}
	}
	if a != b {
		return "", errors.New("passwords do not match")
	}
	if err := auth.ValidatePassword(a); err != nil {
		return "", err
	}
	return a, nil
}

func adminCreate(st *store.Store, dataDir, username, role string, env adminEnv) error {
	r := newReader(env)
	if username == "" {
		username = defaultAdmin
		if r.term {
			fmt.Fprintf(env.out, "Username [%s]: ", defaultAdmin)
			s, err := r.line()
			if err != nil {
				return err
			}
			if s = strings.TrimSpace(s); s != "" {
				username = s
			}
		}
	}
	username = strings.TrimSpace(username)
	if err := auth.ValidateUsername(username); err != nil {
		return err
	}
	if _, err := st.UserByName(username); err == nil {
		return fmt.Errorf("user %q already exists", username)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	pw, err := r.password()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	if _, err := st.CreateUser(username, hash, role); err != nil {
		if errors.Is(err, store.ErrExists) {
			return fmt.Errorf("user %q already exists", username)
		}
		return err
	}
	// Users exist now, so first-run setup is complete.
	if err := os.Remove(filepath.Join(dataDir, "setup-code")); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(env.errw, "nodespot admin: warning: could not remove setup-code: %v\n", err)
	}
	fmt.Fprintf(env.out, "Created %s account %q.\n", role, username)
	return nil
}

func adminResetPassword(st *store.Store, username string, env adminEnv) error {
	u, err := st.UserByName(strings.TrimSpace(username))
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no such user %q (see: nodespot admin list)", username)
	} else if err != nil {
		return err
	}
	pw, err := newReader(env).password()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	if err := st.UpdateUser(u.ID, nil, &hash); err != nil {
		return err
	}
	if err := st.DeleteUserSessions(u.ID, ""); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Password for %q changed; all of that user's sessions were signed out.\n", u.Username)
	return nil
}

func adminList(st *store.Store, quiet bool, env adminEnv) (int, error) {
	users, err := st.ListUsers()
	if err != nil {
		return exitError, err
	}
	if quiet {
		if len(users) == 0 {
			return exitNoUsers, nil
		}
		return exitOK, nil
	}
	if len(users) == 0 {
		fmt.Fprintln(env.out, "No users yet. Create one with: nodespot admin create")
		return exitOK, nil
	}
	tw := tabwriter.NewWriter(env.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "USERNAME\tROLE\tCREATED")
	for _, u := range users {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", u.Username, u.Role, u.Created.Local().Format("2006-01-02 15:04"))
	}
	return exitOK, tw.Flush()
}

// adminSetupCode prints the first-run setup code (stdout: the code only).
// It exits 1 once an account exists.
func adminSetupCode(st *store.Store, dataDir string, env adminEnv) (int, error) {
	n, err := st.CountUsers()
	if err != nil {
		return exitError, err
	}
	if n > 0 {
		fmt.Fprintln(env.errw, "nodespot admin setup-code: setup is already complete (an account exists); if you are locked out, use: nodespot admin reset-password <user>")
		return exitError, nil
	}
	path := filepath.Join(dataDir, "setup-code")
	b, _ := os.ReadFile(path)
	code := strings.TrimSpace(string(b))
	if i := strings.IndexAny(code, "\r\n"); i >= 0 {
		code = code[:i]
	}
	if code == "" {
		as, err := auth.NewService(st, dataDir)
		if err != nil {
			return exitError, err
		}
		if code, err = as.EnsureSetupCode(); err != nil {
			return exitError, err
		}
		fixOwnership(dataDir)
	}
	fmt.Fprintln(env.out, code)
	return exitOK, nil
}
