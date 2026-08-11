package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// readSecret obtains a secret without ever echoing it or letting it reach a
// command-line argument.
//
// Order of preference:
//
//  1. The named environment variable, for non-interactive use (a deploy
//     script, a CI job). An environment variable is not a great place for a
//     secret, but it is strictly better than an argument: it is not in `ps`
//     output on Linux and not in shell history.
//
//  2. An interactive prompt with terminal echo disabled, when stdin is a
//     terminal.
//
//  3. A plain line read from stdin, when it is a pipe — `... | moovctl account
//     add user@example.com` is a legitimate way to feed a password from a
//     secret manager. No prompt is printed in that case, because there is
//     nobody to read it.
//
// The returned string is the caller's to use and discard.
func readSecret(e *env, prompt, envVar string) (string, error) {
	if v := os.Getenv(envVar); v != "" {
		e.logger.Debug("reading the password from the environment", "variable", envVar)
		return v, nil
	}

	f, isFile := e.stdin.(*os.File)
	interactive := isFile && isTerminal(f)

	if !interactive {
		// Piped or redirected input: read one line, no prompt, no echo
		// management to do.
		line, err := bufio.NewReader(e.stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("reading the password from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}

	// The prompt goes to stderr, so that stdout stays a clean data stream even
	// while the command is interactive.
	out(e.stderr, prompt)

	secret, err := readPasswordNoEcho(f)
	outln(e.stderr) // the newline the user's Enter did not echo
	if err != nil {
		return "", err
	}
	return secret, nil
}

// readPasswordNoEcho reads a line from a terminal with echo disabled.
//
// It shells out to `stty` rather than using golang.org/x/term, because E7 is
// stdlib-only by scope decision and x/term is not currently a dependency of
// this module. The trade is deliberate and narrow: the fallback path below
// refuses to read rather than reading with echo on, so the worst case is a
// command that will not run, never a password printed to a shared terminal.
//
// On Windows there is no stty; the documented path there is
// MOOV_ACCOUNT_PASSWORD or a pipe. Moov's operational target is a Linux
// container, so this is a developer-convenience gap rather than a deployment
// one.
func readPasswordNoEcho(f *os.File) (string, error) {
	restore, err := disableEcho()
	if err != nil {
		return "", fmt.Errorf(
			"cannot disable terminal echo, so the password would be visible on screen: %w\n"+
				"Set %s instead, or pipe the password into stdin", err, envAccountPassword)
	}
	defer restore()

	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading the password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// disableEcho turns off terminal echo and returns a function restoring it.
func disableEcho() (restore func(), err error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New("not supported on Windows")
	}

	saved, err := stty("-g")
	if err != nil {
		return nil, err
	}
	if _, err := stty("-echo"); err != nil {
		return nil, err
	}
	return func() {
		// Best effort: if this fails the terminal is left without echo, which
		// is unpleasant but not a security problem, and there is nothing
		// useful to do about it from here.
		_, _ = stty(saved)
	}, nil
}

// stty runs the stty utility against the controlling terminal.
func stty(args ...string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("opening the terminal: %w", err)
	}
	defer func() { _ = tty.Close() }()

	cmd := exec.Command("stty", args...) // #nosec G204 -- args are package constants and stty's own saved-state token.
	cmd.Stdin = tty
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("stty %v: %w", args, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// isTerminal reports whether f is a character device, which is what
// distinguishes a terminal from a pipe or a regular file.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
