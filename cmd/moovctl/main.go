// Command moovctl is the Moov Mail operator CLI.
//
// It performs the administrative actions that are deliberately not exposed as
// a network API: provisioning an account (ADR-001 §4), listing and disabling
// accounts, and generating and rotating the master key that protects stored
// credentials.
//
// # Why a separate binary
//
// moovd, the daemon, holds the master key because it must decrypt credentials
// to sync. It does NOT need the Mailcow admin API key, which can create
// credentials for every mailbox on the server. Keeping provisioning in a
// second binary lets the two secrets live in two places: the long-running
// network-facing process holds the narrower one, and the broader one is
// present only while an operator is running a command.
//
// # Secrets
//
//	MOOV_MASTER_KEY / MOOV_MASTER_KEY_FILE    credential encryption (internal/crypto)
//	MOOV_MAILCOW_API_KEY / _FILE              Mailcow admin API (internal/mailcow)
//	MOOV_DATABASE_URL                         the store
//	MOOV_ACCOUNT_PASSWORD                     a mailbox password, for non-interactive use
//
// No command prints a secret, and none accepts one as a command-line argument:
// arguments are visible in `ps` to every user on the host and are recorded in
// shell history. A password is read from a prompt, or from the environment.
//
// # Exit codes
//
//	0  success
//	1  the command failed
//	2  usage error (unknown command, bad flags)
//	3  invalid credentials — the mailbox password was rejected by the server
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/GrupoNU/moov/internal/provision"
	"github.com/GrupoNU/moov/internal/version"
)

// Exit codes. They are a contract with the scripts that will wrap this.
const (
	exitOK          = 0
	exitFailure     = 1
	exitUsage       = 2
	exitCredentials = 3
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is main's body with its I/O injected, so the whole CLI is testable
// without a subprocess.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("moovctl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version information and exit")
	verbose := fs.Bool("v", false, "log at debug level")
	fs.Usage = func() { printUsage(stderr) }

	if err := fs.Parse(args); err != nil {
		// flag already reported the problem.
		return exitUsage
	}

	if *showVersion {
		outln(stdout, version.Get().String())
		return exitOK
	}

	rest := fs.Args()
	if len(rest) == 0 {
		printUsage(stderr)
		return exitUsage
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	// Logs go to stderr so that stdout carries only the command's output and
	// can be piped into another tool.
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	// Ctrl-C cancels the operation in flight rather than killing the process
	// mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env := &env{stdin: stdin, stdout: stdout, stderr: stderr, logger: logger}

	var err error
	switch rest[0] {
	case "account":
		err = accountCommand(ctx, env, rest[1:])
	case "key":
		err = keyCommand(ctx, env, rest[1:])
	case "help", "-h", "--help":
		printUsage(stdout)
		return exitOK
	default:
		outf(stderr, "moovctl: unknown command %q\n\n", rest[0])
		printUsage(stderr)
		return exitUsage
	}

	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errUsage):
		outf(stderr, "moovctl: %v\n", err)
		return exitUsage
	case errors.Is(err, provision.ErrInvalidCredentials):
		// A distinct code: this is the one failure a script should retry with
		// a different password rather than treat as an outage.
		outf(stderr, "moovctl: %v\n", err)
		return exitCredentials
	case errors.Is(err, context.Canceled):
		outln(stderr, "moovctl: canceled")
		return exitFailure
	default:
		outf(stderr, "moovctl: %v\n", err)
		return exitFailure
	}
}

// env carries the process I/O and logger through the command functions.
type env struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	logger *slog.Logger
}

// errUsage marks an error as a usage problem, which maps to exit code 2.
var errUsage = errors.New("usage")

func usageErrorf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errUsage, fmt.Sprintf(format, args...))
}

func printUsage(w io.Writer) {
	out(w, `moovctl — Moov Mail operator CLI

Usage:
  moovctl [-v] <command> [arguments]

Commands:
  account add <email>     provision an account (ADR-001 §4)
  account list            list the provisioned accounts
  account disable <email> stop syncing an account, keeping its data
  key generate            generate a master key for MOOV_MASTER_KEY
  key rotate              re-seal every stored credential under the primary key

Flags:
  -v          log at debug level
  -version    print version information and exit

Run "moovctl <command> -h" for the flags of a command.

Environment:
  MOOV_DATABASE_URL          PostgreSQL connection string (required)
  MOOV_MASTER_KEY            credential encryption key, base64 (or _FILE)
  MOOV_MAILCOW_BASE_URL      Mailcow API root
  MOOV_MAILCOW_API_KEY       Mailcow read-write API key (or _FILE)
  MOOV_MAILCOW_HOST_HEADER   Host header override, when reaching nginx by IP
  MOOV_IMAP_HOST             Dovecot host for the validation login (default "dovecot")
  MOOV_IMAP_SERVER_NAME      name Dovecot's certificate is verified against
  MOOV_ACCOUNT_PASSWORD      mailbox password, for non-interactive use

No command accepts a secret as an argument: arguments are visible in ps and
recorded in shell history.
`)
}
