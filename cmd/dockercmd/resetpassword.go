package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// Resetting a forgotten password, offline.
//
// There is no other way back: an admin can reset another account's password from
// the UI, but the *last* admin locking themselves out has nobody to ask, and this
// app deliberately gives nobody a way to reset someone else's second factor. Before
// this, that state was terminal.
//
// It is safe to offer because it grants nothing new. The signing secret for session
// tokens (`jwt_secret`) is a row in the same database this command opens, so anyone
// who can run it can already mint themselves an admin session directly — without a
// password, and without a second factor. The capability is the *filesystem*, not
// this command; all this does is make the legitimate use of it survivable.
//
// That argument is what the design has to protect, so:
//
//   - it works only against the data directory, never over HTTP. That does not mean
//     the server has to be stopped: SQLite takes the write either way, and the
//     server re-reads the password and the session epoch per request, so a running
//     instance honours the reset immediately;
//   - the password is read from the terminal, never taken as an argument — an
//     argument lands in shell history and in /proc/<pid>/cmdline, which on most
//     systems any local user can read, and that WOULD be a new leak;
//   - every session for the account is ended, exactly as changing a password in the
//     UI does. A reset that leaves a stolen session alive is the opposite of what
//     someone reaching for it needs;
//   - the second factor is left alone. Whoever holds the files can bypass it
//     anyway, but this command should not do it for them — "nobody resets another
//     account's second factor" stays true, and stays easy to state.
//
// The reset is written to the audit log like any other privileged action.

// resetPasswordUser returns the username given to --reset-password, or "".
func resetPasswordUser() string {
	args := os.Args[1:]
	for i, a := range args {
		if a == "--" {
			break
		}
		if a != "-reset-password" && a != "--reset-password" {
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			return args[i+1]
		}
		return "" // the flag without a name: handled as an error by the caller
	}
	return ""
}

// wantsResetPassword reports whether --reset-password was passed at all, so the
// flag with no username can be told apart from the flag being absent.
func wantsResetPassword() bool {
	for _, a := range os.Args[1:] {
		if a == "--" {
			break
		}
		if a == "-reset-password" || a == "--reset-password" {
			return true
		}
	}
	return false
}

// runResetPassword prompts for the new password, then applies it.
//
// The prompt and the work are separate so the part with the security properties —
// sessions ended, epoch bumped, factor untouched — can be tested without a
// terminal.
func runResetPassword(dataDir, username string) error {
	if username == "" {
		return errors.New("usage: dockercmd --reset-password <username>")
	}
	// The prompt comes FIRST, before the database is opened. Opening it creates
	// -wal and -shm files, and this prompt waits for a human — a Ctrl-C at it would
	// otherwise leave those behind owned by whoever ran the command. On a packaged
	// install that is root, in a directory owned by the service user, and the
	// service can then no longer write its own database. A failed recovery attempt
	// must not be worse than no attempt.
	fmt.Printf("Resetting a password in %s\n", dataDir)
	pw, err := readNewPassword()
	if err != nil {
		return err
	}

	u, st, err := openForReset(dataDir, username)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := applyResetTo(st, u, pw); err != nil {
		return err
	}
	fmt.Printf("Done — %q now has the new password.\n", u.Username)
	fmt.Println("Every browser session for that account has been signed out.")
	// Deliberately named rather than folded into "every session": API and MCP
	// tokens are not sessions and are NOT revoked by this, and somebody running
	// this while recovering from a suspected compromise needs to know that.
	fmt.Println("API and MCP tokens are NOT revoked — review them in the UI if this was a compromise.")
	if u.MFAEnabled {
		fmt.Println("Its second factor is unchanged, so signing in still asks for it —")
		fmt.Println("unless this instance has the localhost 2FA exemption on and you sign in from the machine itself.")
	}
	return nil
}

// openForReset resolves the account and returns the open store with it.
func openForReset(dataDir, username string) (*store.User, *store.Store, error) {
	db := filepath.Join(dataDir, "docker-commander.db")
	// store.Open would happily CREATE one. On a packaged install the data dir comes
	// from /etc/docker-commander/commander.conf, which this path does not read — so
	// `sudo dockercmd --reset-password admin` lands in root's own config directory,
	// and creating an empty database there answers "no account called admin",
	// which is a lie told to someone who is locked out and in a hurry.
	if _, err := os.Stat(db); err != nil {
		return nil, nil, fmt.Errorf("no database at %s — point at the right one with --data-dir "+
			"(a packaged install uses /var/lib/dockercmd)", db)
	}
	st, err := store.Open(db)
	if err != nil {
		return nil, nil, fmt.Errorf("opening the database in %s: %w", dataDir, err)
	}
	ctx := context.Background()
	u, err := st.UserByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		st.Close()
		return nil, nil, fmt.Errorf("no account called %q in %s", username, dataDir)
	}
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	// Only accounts whose password this app owns. An allowlist, matching the
	// passwordless sign-in path: a directory account's password lives in the
	// directory, and writing a local one would store a credential the login path
	// never consults. A denylist of "ldap" would silently accept the next auth
	// source somebody adds.
	if u.AuthSource != "" && u.AuthSource != "local" {
		st.Close()
		return nil, nil, fmt.Errorf("%q is a %s account — its password belongs to that directory, not here",
			username, u.AuthSource)
	}
	return u, st, nil
}

// applyResetTo writes the new password and ends the account's sessions.
func applyResetTo(st *store.Store, u *store.User, pw string) error {
	// Enforced HERE, not only in the prompt: the prompt is one caller, and a floor
	// that lives only in the part a test bypasses is a floor the next caller does
	// not have. The same minimum the UI applies.
	if len(pw) < auth.MinPasswordLength {
		return fmt.Errorf("a password must be at least %d characters", auth.MinPasswordLength)
	}
	ctx := context.Background()
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	if err := st.UpdatePassword(ctx, u.ID, hash); err != nil {
		return err
	}
	// Both, and in this order: the epoch voids tokens whose session row is already
	// gone, and dropping the rows keeps the account's own session list honest. This
	// mirrors what auth.Service.SetPassword does, because a reset that leaves the
	// old sessions working is not a reset.
	if err := st.DeleteUserSessions(ctx, u.ID); err != nil {
		return err
	}
	if err := st.BumpSessionEpoch(ctx, u.ID); err != nil {
		return err
	}
	if err := st.Audit(ctx, store.AuditEntry{
		UserID: u.ID, Username: u.Username, Action: "auth.password.reset",
		Target: u.Username, Detail: "offline reset via --reset-password", IP: "local",
	}); err != nil {
		// The password is already changed; refusing now would leave the operator
		// unsure which half happened.
		fmt.Fprintf(os.Stderr, "warning: the reset succeeded but could not be written to the audit log: %v\n", err)
	}

	return nil
}

// readNewPassword prompts twice, without echo.
func readNewPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// Reading from a pipe would mean the password came from somewhere it can be
		// recovered from — a script, a history file, a CI log.
		return "", errors.New("this needs a terminal so the password is never in a pipe, a script or a log — " +
			"use `ssh -t host dockercmd --reset-password …` or `docker exec -it <container> dockercmd …`")
	}
	fmt.Print("New password: ")
	first, err := term.ReadPassword(fd)
	fmt.Println()
	if err != nil {
		return "", err
	}
	fmt.Print("Repeat it: ")
	second, err := term.ReadPassword(fd)
	fmt.Println()
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("the two entries did not match")
	}
	// The same floor the UI enforces; a reset must not be a way around it.
	if len(first) < auth.MinPasswordLength {
		return "", fmt.Errorf("a password must be at least %d characters", auth.MinPasswordLength)
	}
	return string(first), nil
}
