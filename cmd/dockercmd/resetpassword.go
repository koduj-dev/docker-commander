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
	u, st, err := openForReset(dataDir, username)
	if err != nil {
		return err
	}
	defer st.Close()

	fmt.Printf("Resetting the password for %q in %s\n", u.Username, dataDir)
	pw, err := readNewPassword()
	if err != nil {
		return err
	}
	if err := applyResetTo(st, u, pw); err != nil {
		return err
	}
	fmt.Println("Done. Every session for that account has been signed out.")
	if u.MFAEnabled {
		fmt.Println("Its second factor is unchanged — you will still be asked for it when signing in.")
	}
	return nil
}

// openForReset resolves the account and returns the open store with it.
func openForReset(dataDir, username string) (*store.User, *store.Store, error) {
	st, err := store.Open(filepath.Join(dataDir, "docker-commander.db"))
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
		return "", errors.New("this needs a terminal: run it directly, not through a pipe")
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
