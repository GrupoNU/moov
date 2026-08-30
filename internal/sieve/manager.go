package sieve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// The manager: the takeover-safe orchestration of the ONE Moov-managed
// script against a connected Client. Every rule the epic's invariants state
// is enforced here, in one place:
//
//   - Dovecot is the source of truth: Read pulls the script and parses it;
//     nothing about rules is read from anywhere else.
//   - Never destroy what we did not write: a foreign ACTIVE script is never
//     deactivated silently — its content is imported verbatim into the
//     managed script's external section (requires merged), a backup copy is
//     stored under a distinct name, and only then is ours activated. Global
//     sieve_before/sieve_after scripts are invisible to ManageSieve and
//     therefore untouchable by construction; the USER script slot is the
//     only thing managed here.
//   - Our own script, hand-edited: the drifted bytes are backed up before
//     the regeneration overwrites them.

// ManagedScriptName is the name of the Moov-managed script.
const ManagedScriptName = "moov"

// BackupPrefix prefixes the safety copies made before any takeover.
const BackupPrefix = "moov-backup-"

// ManagedState is what Read learned about the account's script storage.
type ManagedState struct {
	// Model is the parsed managed script, or an empty model when none exists
	// yet. Never nil.
	Model *Script

	// Exists reports whether the managed script is stored at all.
	Exists bool

	// Drifted reports that the stored managed script was edited outside
	// Moov (parse.go's byte comparison).
	Drifted bool

	// ActiveScript is the name of the currently active script, "" when none.
	ActiveScript string

	// OursActive is true when the managed script is the active one — the
	// condition under which the model's rules are actually filtering mail.
	// The JMAP layer surfaces this honestly instead of pretending.
	OursActive bool

	// Raw is the stored managed script's bytes, for backup decisions.
	Raw []byte
}

// Manager orchestrates the managed script over one connected Client. It
// holds no state of its own; every operation reads the server first.
type Manager struct {
	c   Client
	log *slog.Logger
}

// NewManager wraps a connected client.
func NewManager(c Client, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{c: c, log: logger}
}

// Read pulls the current managed state.
func (m *Manager) Read(ctx context.Context, env GenerateEnv) (*ManagedState, error) {
	scripts, err := m.c.ListScripts(ctx)
	if err != nil {
		return nil, err
	}
	state := &ManagedState{Model: &Script{Version: MetadataVersion}}
	for _, s := range scripts {
		if s.Active {
			state.ActiveScript = s.Name
			state.OursActive = s.Name == ManagedScriptName
		}
		if s.Name == ManagedScriptName {
			state.Exists = true
		}
	}
	if !state.Exists {
		return state, nil
	}

	raw, err := m.c.GetScript(ctx, ManagedScriptName)
	if err != nil {
		return nil, err
	}
	state.Raw = raw

	model, drifted, err := ParseManaged(raw, env)
	switch {
	case errors.Is(err, ErrNotManaged):
		// Someone stored their own script under our name. Its content is
		// theirs: treat it as foreign (drifted), to be imported and backed
		// up by the next Push. The model stays empty.
		state.Drifted = true
	case err != nil:
		return nil, err
	default:
		state.Model = model
		state.Drifted = drifted
	}
	return state, nil
}

// Push regenerates and stores the managed script from model, then makes it
// active — after performing whatever preservation the current server state
// requires. It returns the server's WARNINGS diagnostic when there is one.
//
// The preservation ladder, in order:
//
//  1. The stored managed script drifted (hand-edited or overwritten): its
//     exact bytes are backed up as "moov-backup-moov" before regeneration.
//     If it carries no Moov metadata at all, it is ALSO imported into the
//     external section — those are someone's rules, and they keep running.
//  2. A DIFFERENT script is active: imported verbatim into the external
//     section (requires lifted and merged), backed up as
//     "moov-backup-<name>", and left stored untouched. Only after both does
//     SETACTIVE move to ours. The original remains as an inactive script
//     the user can reactivate at any time — reactivating it simply parks
//     Moov's filtering again (Read reports OursActive false).
func (m *Manager) Push(ctx context.Context, model *Script, env GenerateEnv) (warnings string, err error) {
	env = env.withDefaults()
	caps := m.c.Capabilities()

	state, err := m.Read(ctx, env)
	if err != nil {
		return "", err
	}

	// Step 1: our own slot, edited by someone else's hand.
	if state.Exists && state.Drifted {
		if err := m.backup(ctx, ManagedScriptName, state.Raw); err != nil {
			return "", err
		}
		if _, _, perr := ParseManaged(state.Raw, env); errors.Is(perr, ErrNotManaged) {
			ImportForeign(model, ManagedScriptName, state.Raw)
		}
	}

	// Step 2: a foreign active script.
	if state.ActiveScript != "" && state.ActiveScript != ManagedScriptName {
		foreign, gerr := m.c.GetScript(ctx, state.ActiveScript)
		if gerr != nil {
			return "", fmt.Errorf("reading the active script %q before takeover: %w", state.ActiveScript, gerr)
		}
		if err := m.backup(ctx, state.ActiveScript, foreign); err != nil {
			return "", err
		}
		ImportForeign(model, state.ActiveScript, foreign)
		m.log.Info("sieve: importing the active foreign script before takeover",
			"script", state.ActiveScript, "bytes", len(foreign))
	}

	model.Version = MetadataVersion
	content, err := Generate(model, env, &caps)
	if err != nil {
		return "", err
	}

	// CHECKSCRIPT first: a refused script leaves the stored one untouched,
	// so a generator bug can break an UPDATE but never break FILTERING.
	if _, err := m.c.CheckScript(ctx, content); err != nil {
		return "", fmt.Errorf("the generated script failed the server's validation: %w", err)
	}
	warnings, err = m.c.PutScript(ctx, ManagedScriptName, content)
	if err != nil {
		return "", err
	}
	if err := m.c.SetActive(ctx, ManagedScriptName); err != nil {
		return warnings, fmt.Errorf("stored but could not activate: %w", err)
	}
	return warnings, nil
}

// backup stores a safety copy under BackupPrefix+name. A name that already
// carries the prefix is not double-prefixed (backing up a backup would breed
// names without bound).
func (m *Manager) backup(ctx context.Context, name string, content []byte) error {
	backupName := name
	if !strings.HasPrefix(backupName, BackupPrefix) {
		backupName = BackupPrefix + name
	}
	// RFC 5804 §1.6: names must fit 128 Unicode characters; truncate the
	// suffix rather than fail a takeover over a long foreign name.
	if runes := []rune(backupName); len(runes) > 128 {
		backupName = string(runes[:128])
	}
	if _, err := m.c.PutScript(ctx, backupName, content); err != nil {
		return fmt.Errorf("storing the safety copy %q: %w", backupName, err)
	}
	m.log.Info("sieve: stored a safety copy before takeover", "backup", backupName, "bytes", len(content))
	return nil
}
