package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Mailbox/set — RFC 8620 §5.3 over the RFC 8621 §2.5 Mailbox object.
//
// # What is settable, and what the server keeps
//
// §2.5: "The Mailbox object is... created, updated and destroyed via the
// standard set method", with three server-controlled exceptions this
// implementation honors:
//
//   - role. §2 types it as "immutable; server-set" and §2.5 says the server
//     "MAY reject" an attempt to set one. Moov rejects: a role is a
//     SPECIAL-USE attribute Dovecot assigns, the store enforces exactly one
//     mailbox per role per account (a partial unique index), and a client
//     minting a second \Sent would produce a tree the store cannot hold. The
//     rejection is explicit and names the reason, never a silent drop.
//   - sortOrder. §2 makes it optional and server-computed here (adapter.go's
//     sortOrderFor gives the special-use folders the order every mail client
//     shows them in); a client setting it would be writing to a column that
//     does not exist.
//   - the four count properties and myRights, which §2 types as server-set.
//
// name, parentId and isSubscribed are the settable three, and they are what a
// user actually manipulates: create a folder, move it under another, rename
// it, hide it from the folder list.
//
// # The two deliberate deviations from §2.5, stated plainly
//
// 1. DESTROY OF A NON-EMPTY MAILBOX GOES THROUGH TRASH, NOT THROUGH A PERMANENT
//    EXPUNGE. §2.5 defines onDestroyRemoveEmails as: "If false, any attempt to
//    destroy a Mailbox that still has Emails in it will be rejected with a
//    mailboxHasEmail SetError. If true, any Emails that were in the Mailbox
//    will be removed from it, and they will be destroyed when no Mailbox
//    contains them any longer."
//
//    Moov implements the false branch exactly. For the true branch it moves the
//    messages to \Trash first and then deletes the now-empty folder, instead of
//    destroying them outright. That is a REAL deviation and it is chosen on
//    purpose: it is the same arbitrage W-A2 already made for Email/set destroy
//    ("destroy = mover a Trash; expunge solo desde Trash"), for the same
//    reason — the pilot account is the product owner's real mailbox, and a
//    misplaced click that permanently destroys a folder's worth of mail is not
//    a failure mode this product is willing to have. The user-visible result is
//    strictly safer and matches what Gmail, Fastmail and every client the
//    Gmail-class bar points at do; the messages remain destroyable from Trash,
//    which is where §2.5's "destroyed when no Mailbox contains them" outcome
//    can still be reached deliberately. A client relying on the letter of §2.5
//    sees its Emails' mailboxIds change to Trash rather than the Emails
//    vanishing — a difference it discovers by reading, not by losing data.
//
// 2. PROTECTED ROLES REFUSE BOTH DESTROY AND RENAME. §2.5 says only "The server
//    MAY reject the destruction of the Mailbox if it has a role". Moov extends
//    that refusal to rename for the role mailboxes an account cannot function
//    without — inbox, trash, sent, drafts, junk, archive — because Dovecot's
//    SPECIAL-USE assignment and the account's Sieve rules key on those folders,
//    and renaming \Sent out from under the submission path (W3) would break
//    sending in a way the user could not diagnose. The refusal is per-mailbox
//    and truthful: myRights reports mayRename:false and mayDelete:false for
//    exactly these, so a conforming client does not offer the action at all.
//
// # Per-id error isolation
//
// As in Email/set: one bad id never fails the batch, every create/update/
// destroy answers for itself through notCreated/notUpdated/notDestroyed, and
// the state strings bracket the whole call.

// The SetError types §2.5 defines for Mailbox/set specifically, on top of the
// §5.3 vocabulary in set.go.
const (
	// setErrMailboxHasEmail — §2.5: "The Mailbox still has at least one Email
	// in it and the onDestroyRemoveEmails argument was false."
	setErrMailboxHasEmail = "mailboxHasEmail"

	// setErrMailboxHasChild — §2.5: "The Mailbox still has at least one child
	// Mailbox. The client MUST remove these before it can delete the parent
	// Mailbox."
	setErrMailboxHasChild = "mailboxHasChild"
)

// mailboxSetRequest is setRequest plus Mailbox/set's one extra argument.
type mailboxSetRequest struct {
	// OnDestroyRemoveEmails — §2.5, default false.
	OnDestroyRemoveEmails bool `json:"onDestroyRemoveEmails"`
}

// protectedRoles are the roles whose mailbox may be neither renamed nor
// destroyed (see the deviation note above).
//
// "all" and "flagged" are deliberately NOT here: they are virtual folders on
// the servers that have them, so the question does not arise, and hard-coding
// a refusal for a folder Dovecot may not even present would be a guess.
var protectedRoles = map[string]bool{
	"inbox":   true,
	"trash":   true,
	"sent":    true,
	"drafts":  true,
	"junk":    true,
	"archive": true,
}

// isProtectedRole reports whether a stored role is one of the untouchable ones.
func isProtectedRole(role string) bool { return protectedRoles[strings.ToLower(role)] }

// handleMailboxSet implements Mailbox/set.
func (d *Deps) handleMailboxSet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseSet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}

	var extra mailboxSetRequest
	if err := json.Unmarshal(args, &extra); err != nil {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("arguments did not parse: %v", err)
	}

	oldState, err := d.State.MailboxState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading mailbox state", err)
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, jmap.NewMethodError(jmap.CodeStateMismatch).
			WithDescription("ifInState does not match the current Mailbox state; fetch changes and retry")
	}

	resp := &setResponse{AccountID: req.AccountID, OldState: oldState}

	// The whole tree, read once. Every operation below needs it — to resolve a
	// parentId to a path, to detect a name collision, to find children, to
	// check a role — and re-reading it per id would be N reads for one answer
	// that cannot change under us within a single /set (the writer holds the
	// account's connection lock).
	//
	// It is re-read after each mutation instead, because a create changes what
	// the NEXT create in the same batch can be a child of, and a client that
	// creates a parent and its child in one call (with a #creationId back
	// reference) has every right to expect that to work.
	tree, merr := d.readTree(ctx, caller.AccountID)
	if merr != nil {
		return nil, merr
	}

	// ---- create, in sorted creation-id order for a reproducible response ----
	createIDs := make([]string, 0, len(req.Create))
	for cid := range req.Create {
		createIDs = append(createIDs, cid)
	}
	sort.Strings(createIDs)

	creationIDs := jmap.CreationIDsFromContext(ctx)
	for _, cid := range createIDs {
		created, serr := d.applyMailboxCreate(ctx, caller.AccountID, tree, req.Create[cid])
		if serr != nil {
			if resp.NotCreated == nil {
				resp.NotCreated = map[string]setError{}
			}
			resp.NotCreated[cid] = *serr
			continue
		}
		if resp.Created == nil {
			resp.Created = map[string]any{}
		}
		// §5.3: the created object must carry "any properties that were not
		// set by the client" — here the server-assigned id, role, sortOrder and
		// the counts, which are all zero for a brand-new empty folder.
		resp.Created[cid] = created
		// §3.3: the request's creation-id map learns every /set create, so a
		// later call (an Email/set filing a draft into "#newFolder") resolves
		// it.
		if wire, ok := created["id"].(string); ok {
			creationIDs.Record(cid, wire)
		}
		if tree, merr = d.readTree(ctx, caller.AccountID); merr != nil {
			return nil, merr
		}
	}

	destroySet := make(map[string]bool, len(req.Destroy))
	for _, id := range req.Destroy {
		destroySet[id] = true
	}

	// ---- update ------------------------------------------------------------
	updateIDs := make([]string, 0, len(req.Update))
	for id := range req.Update {
		updateIDs = append(updateIDs, id)
	}
	sort.Strings(updateIDs)

	for _, wire := range updateIDs {
		if destroySet[wire] {
			d.noteNotUpdated(resp, wire, setError{Type: setErrWillDestroy,
				Description: "the same id is also in destroy; the update was ignored"})
			continue
		}
		if serr := d.applyMailboxUpdate(ctx, caller.AccountID, tree, wire, req.Update[wire]); serr != nil {
			d.noteNotUpdated(resp, wire, *serr)
			continue
		}
		if resp.Updated == nil {
			resp.Updated = map[string]any{}
		}
		resp.Updated[wire] = nil
		if tree, merr = d.readTree(ctx, caller.AccountID); merr != nil {
			return nil, merr
		}
	}

	// ---- destroy -----------------------------------------------------------
	seen := make(map[string]bool, len(req.Destroy))
	for _, wire := range req.Destroy {
		if seen[wire] {
			continue
		}
		seen[wire] = true
		if serr := d.applyMailboxDestroy(ctx, caller.AccountID, tree, wire, extra.OnDestroyRemoveEmails); serr != nil {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = map[string]setError{}
			}
			resp.NotDestroyed[wire] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, wire)
		if tree, merr = d.readTree(ctx, caller.AccountID); merr != nil {
			return nil, merr
		}
	}

	newState, err := d.State.MailboxState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading mailbox state", err)
	}
	resp.NewState = newState
	return resp, nil
}

// ---------------------------------------------------------------------------
// the tree
// ---------------------------------------------------------------------------

// mailboxTree is the account's folder tree as one /set call sees it.
type mailboxTree struct {
	rows  []MailboxRow
	byID  map[int64]MailboxRow
	paths map[int64]string // mailbox id -> full IMAP path
	delim string
}

// readTree loads the account's mailboxes and derives every full IMAP path.
func (d *Deps) readTree(ctx context.Context, accountID int64) (*mailboxTree, *jmap.MethodError) {
	rows, err := d.Mailboxes.Mailboxes(ctx, accountID)
	if err != nil {
		return nil, serverFail("listing mailboxes", err)
	}
	t := &mailboxTree{
		rows:  rows,
		byID:  make(map[int64]MailboxRow, len(rows)),
		paths: make(map[int64]string, len(rows)),
		delim: d.delimiter(),
	}
	for _, r := range rows {
		t.byID[r.ID] = r
	}
	for _, r := range rows {
		t.paths[r.ID] = t.pathOf(r.ID)
	}
	return t, nil
}

// delimiter is the hierarchy separator this deployment composes paths with.
//
// The reader contract does not carry it (MailboxRow has no Delimiter field:
// the JMAP layer has no use for one when READING — parentId already expresses
// the hierarchy), so it is configurable with the near-universal "/" as the
// default. Our Dovecot uses "/" (measured in S1/S2), and the value only ever
// composes a name for the executor, which validates it against the server.
func (d *Deps) delimiter() string {
	if d.MailboxDelimiter != "" {
		return d.MailboxDelimiter
	}
	return "/"
}

// pathOf rebuilds a mailbox's full IMAP path by walking its parents.
//
// The walk is bounded by the number of mailboxes: a cycle in the stored tree
// is impossible (the hierarchy is derived from the name, so it is a tree by
// construction), but the bound is written anyway because this walks data from
// a database and a bound that costs nothing beats an infinite loop in a mail
// server.
func (t *mailboxTree) pathOf(id int64) string {
	segments := []string{}
	for cur, hops := id, 0; cur != 0 && hops <= len(t.byID); hops++ {
		row, ok := t.byID[cur]
		if !ok {
			break
		}
		segments = append(segments, row.Name)
		cur = row.ParentID
	}
	for i, j := 0, len(segments)-1; i < j; i, j = i+1, j-1 {
		segments[i], segments[j] = segments[j], segments[i]
	}
	return strings.Join(segments, t.delim)
}

// childPath composes the full path a mailbox named leaf under parent would
// have. A zero parent means top level.
func (t *mailboxTree) childPath(parentID int64, leaf string) string {
	if parentID == 0 {
		return leaf
	}
	if p, ok := t.paths[parentID]; ok && p != "" {
		return p + t.delim + leaf
	}
	return leaf
}

// findByPath returns the mailbox at an exact IMAP path.
func (t *mailboxTree) findByPath(path string) (MailboxRow, bool) {
	for id, p := range t.paths {
		if p == path {
			return t.byID[id], true
		}
	}
	return MailboxRow{}, false
}

// descendants returns the mailboxes strictly below the given one.
func (t *mailboxTree) descendants(id int64) []MailboxRow {
	var out []MailboxRow
	for _, r := range t.rows {
		if r.ID == id {
			continue
		}
		for cur, hops := r.ParentID, 0; cur != 0 && hops <= len(t.byID); hops++ {
			if cur == id {
				out = append(out, r)
				break
			}
			parent, ok := t.byID[cur]
			if !ok {
				break
			}
			cur = parent.ParentID
		}
	}
	return out
}

// isDescendantOf reports whether candidate is at or below ancestor — the
// cycle check a re-parent needs.
func (t *mailboxTree) isDescendantOf(candidate, ancestor int64) bool {
	if candidate == ancestor {
		return true
	}
	for cur, hops := candidate, 0; cur != 0 && hops <= len(t.byID); hops++ {
		row, ok := t.byID[cur]
		if !ok {
			return false
		}
		if row.ParentID == ancestor {
			return true
		}
		cur = row.ParentID
	}
	return false
}

// ---------------------------------------------------------------------------
// create
// ---------------------------------------------------------------------------

// mailboxCreate is one create object after interpretation.
type mailboxCreate struct {
	name         string
	parentID     int64
	isSubscribed bool
}

// applyMailboxCreate creates one mailbox.
func (d *Deps) applyMailboxCreate(ctx context.Context, accountID int64, tree *mailboxTree, raw json.RawMessage) (map[string]any, *setError) {
	spec, serr := interpretMailboxCreate(tree, raw)
	if serr != nil {
		return nil, serr
	}

	path := tree.childPath(spec.parentID, spec.name)
	if _, taken := tree.findByPath(path); taken {
		// §2.5: "invalidProperties: ... a Mailbox already exists with this name
		// and parent". The RFC's own condition, named on the property.
		return nil, &setError{Type: setErrInvalidProperties, Properties: []string{"name"},
			Description: "a mailbox with that name already exists under that parent"}
	}

	id, err := d.Mailboxer.CreateMailbox(ctx, accountID, path, spec.isSubscribed)
	if err != nil {
		return nil, mailboxSetError(err, "creating the mailbox")
	}

	// §5.3: the created object carries the properties the server assigned. A
	// brand-new folder is empty and roleless, and its rights are whatever this
	// server grants an ordinary folder — which is the honest answer to "what
	// can I do with what I just made".
	return map[string]any{
		"id":            EncodeMailboxID(id),
		"role":          nil,
		"sortOrder":     sortOrderForRole(""),
		"totalEmails":   uint64(0),
		"unreadEmails":  uint64(0),
		"totalThreads":  uint64(0),
		"unreadThreads": uint64(0),
		"myRights":      rightsFor(""),
	}, nil
}

// interpretMailboxCreate validates one create object.
func interpretMailboxCreate(tree *mailboxTree, raw json.RawMessage) (*mailboxCreate, *setError) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, &setError{Type: setErrInvalidProperties,
			Description: "a create must be a Mailbox object (RFC 8621 §2.5)"}
	}

	out := &mailboxCreate{
		// §2: isSubscribed defaults to... nothing the RFC fixes, but a folder
		// a user just created and cannot see in their client is a bug report.
		// Subscribing by default is what every mail client does on create.
		isSubscribed: true,
	}
	var badProps []string

	for key, val := range obj {
		switch key {
		case "name":
			var name string
			if err := json.Unmarshal(val, &name); err != nil {
				badProps = append(badProps, "name")
				continue
			}
			out.name = name

		case "parentId":
			// §2: "The Mailbox id for the parent of this Mailbox, or null if
			// this Mailbox is at the top level."
			if strings.TrimSpace(string(val)) == "null" {
				out.parentID = 0
				continue
			}
			var wire string
			if err := json.Unmarshal(val, &wire); err != nil {
				badProps = append(badProps, "parentId")
				continue
			}
			id, err := DecodeMailboxID(wire)
			if err != nil {
				badProps = append(badProps, "parentId")
				continue
			}
			if _, ok := tree.byID[id]; !ok {
				// An id this account does not hold. §5.3 wants the offending
				// property named; the account scoping means "unknown" and
				// "someone else's" are the same answer, as everywhere else.
				badProps = append(badProps, "parentId")
				continue
			}
			out.parentID = id

		case "isSubscribed":
			var sub bool
			if err := json.Unmarshal(val, &sub); err != nil {
				badProps = append(badProps, "isSubscribed")
				continue
			}
			out.isSubscribed = sub

		case "role":
			// §2 types role as "immutable; server-set", and §2.5 allows the
			// server to reject an attempt to set one. Rejected loudly rather
			// than ignored: a client that believes it created the Archive
			// folder and finds it roleless later has a harder bug than one
			// that got told no.
			if strings.TrimSpace(string(val)) == "null" {
				continue // explicitly asking for no role is what we do anyway
			}
			return nil, &setError{Type: setErrInvalidProperties, Properties: []string{"role"},
				Description: "role is server-set (RFC 8621 §2: immutable; server-set): " +
					"SPECIAL-USE roles come from the IMAP server, and this account holds at most one mailbox per role"}

		case "id", "sortOrder", "totalEmails", "unreadEmails", "totalThreads", "unreadThreads", "myRights":
			// §2 types every one of these as server-set. A client sending them
			// back — which a round-tripping client does — must be told which,
			// not silently obeyed.
			badProps = append(badProps, key)

		default:
			badProps = append(badProps, key)
		}
	}

	if len(badProps) > 0 {
		sort.Strings(badProps)
		return nil, &setError{Type: setErrInvalidProperties, Properties: badProps,
			Description: "only name, parentId and isSubscribed may be set on a Mailbox (RFC 8621 §2)"}
	}
	if serr := validateLeafName(out.name, tree.delim); serr != nil {
		return nil, serr
	}
	return out, nil
}

// validateLeafName enforces what a JMAP Mailbox name may be.
//
// RFC 8621 §2: "name: String — User-visible name for the Mailbox... This MUST
// be a Net-Unicode string of at least 1 character in length, subject to the
// maximum size given in the capability object." Two extra refusals are Moov's
// and are named as such:
//
//   - a name containing the hierarchy delimiter. JMAP expresses hierarchy with
//     parentId and §2 is explicit that name "MUST NOT be the full path"; a
//     leaf carrying "/" would silently create a folder at a different place in
//     the tree than the client asked for.
//   - CR, LF and NUL, which cannot appear in an IMAP mailbox name at all
//     (RFC 3501 §2.2) and would otherwise reach the executor as a protocol
//     error whose text tells the user nothing.
func validateLeafName(name, delim string) *setError {
	switch {
	case name == "":
		return &setError{Type: setErrInvalidProperties, Properties: []string{"name"},
			Description: "name is required and must be at least 1 character (RFC 8621 §2)"}
	case strings.TrimSpace(name) == "":
		return &setError{Type: setErrInvalidProperties, Properties: []string{"name"},
			Description: "name cannot be only whitespace"}
	case delim != "" && strings.Contains(name, delim):
		return &setError{Type: setErrInvalidProperties, Properties: []string{"name"},
			Description: "name is the leaf name, not the full path (RFC 8621 §2): " +
				"use parentId to place the mailbox in the tree, and do not include " + delim}
	case strings.ContainsAny(name, "\r\n\x00"):
		return &setError{Type: setErrInvalidProperties, Properties: []string{"name"},
			Description: "name contains a character an IMAP mailbox name cannot carry (RFC 3501 §2.2)"}
	case len(name) > maxMailboxNameBytes:
		return &setError{Type: setErrInvalidProperties, Properties: []string{"name"},
			Description: fmt.Sprintf("name is %d bytes, over this server's %d-byte limit", len(name), maxMailboxNameBytes)}
	}
	return nil
}

// maxMailboxNameBytes bounds a leaf name. It mirrors internal/imap's own cap
// (which is stated on the full path); restating it here rather than importing
// keeps the layering rule — this package never imports internal/imap.
const maxMailboxNameBytes = 512

// ---------------------------------------------------------------------------
// update
// ---------------------------------------------------------------------------

// applyMailboxUpdate applies one update object to one mailbox.
func (d *Deps) applyMailboxUpdate(ctx context.Context, accountID int64, tree *mailboxTree, wire string, raw json.RawMessage) *setError {
	id, err := DecodeMailboxID(wire)
	if err != nil {
		return &setError{Type: setErrNotFound}
	}
	row, ok := tree.byID[id]
	if !ok {
		return &setError{Type: setErrNotFound}
	}

	upd, serr := interpretMailboxPatch(raw)
	if serr != nil {
		return serr
	}

	// Subscription first: it is independent of the path, so applying it before
	// a rename means a failed rename does not also lose the subscription
	// change the client asked for in the same object.
	if upd.sawSubscribed && upd.isSubscribed != row.IsSubscribed {
		// Subscription is not yet a separate write path — the executor sets it
		// on create. Refusing is the honest answer rather than reporting a
		// success the server never saw. (W4 or the PWA epic gives it a path;
		// until then, a client that toggles it learns why.)
		return &setError{Type: setErrServerUnavailable, Properties: []string{"isSubscribed"},
			Description: "changing isSubscribed on an existing mailbox is not available yet; " +
				"it is set when the mailbox is created"}
	}

	if !upd.touchesPath() {
		return nil // nothing left to do; a no-op update is a success
	}

	// The new leaf and the new parent, defaulting to what the mailbox has.
	newLeaf := row.Name
	if upd.sawName {
		newLeaf = upd.name
	}
	newParent := row.ParentID
	if upd.sawParent {
		newParent = upd.parentID
	}

	if serr := validateLeafName(newLeaf, tree.delim); serr != nil {
		return serr
	}

	// A protected role may not be renamed OR re-parented (see the header note).
	if isProtectedRole(row.Role) {
		return &setError{Type: setErrForbidden, Properties: []string{"name", "parentId"},
			Description: fmt.Sprintf("the %s mailbox is a role folder this account depends on "+
				"and cannot be renamed or moved; myRights reports mayRename:false for it", row.Role)}
	}

	if upd.sawParent && newParent != 0 {
		parent, ok := tree.byID[newParent]
		if !ok {
			return &setError{Type: setErrInvalidProperties, Properties: []string{"parentId"},
				Description: "no such parent mailbox"}
		}
		// §2.5's cycle rule: "invalidProperties: ... The parentId is a
		// descendant of this Mailbox." Moving a folder inside its own subtree
		// has no meaning and would detach the whole subtree from the tree.
		if tree.isDescendantOf(newParent, id) {
			return &setError{Type: setErrInvalidProperties, Properties: []string{"parentId"},
				Description: "the new parent is this mailbox or one of its descendants; " +
					"a mailbox cannot contain itself (RFC 8621 §2.5)"}
		}
		_ = parent
	}

	newPath := tree.childPath(newParent, newLeaf)
	oldPath := tree.paths[id]
	if newPath == oldPath {
		return nil // an update that asks for the state the mailbox is in
	}
	if other, taken := tree.findByPath(newPath); taken && other.ID != id {
		return &setError{Type: setErrInvalidProperties, Properties: []string{"name", "parentId"},
			Description: "a mailbox with that name already exists under that parent"}
	}

	if err := d.Mailboxer.RenameMailbox(ctx, accountID, id, newPath); err != nil {
		return mailboxSetError(err, "renaming the mailbox")
	}
	return nil
}

// mailboxUpdate is one update object after interpretation.
type mailboxUpdate struct {
	name          string
	sawName       bool
	parentID      int64
	sawParent     bool
	isSubscribed  bool
	sawSubscribed bool
}

func (u *mailboxUpdate) touchesPath() bool { return u.sawName || u.sawParent }

// interpretMailboxPatch parses one §5.3 PatchObject for a Mailbox.
//
// Mailbox has no set-valued or nested properties, so a pointer with a sub-key
// can never be valid — which is why the sub-key case is invalidPatch rather
// than invalidProperties.
func interpretMailboxPatch(raw json.RawMessage) (*mailboxUpdate, *setError) {
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(raw, &patch); err != nil || patch == nil {
		return nil, &setError{Type: setErrInvalidProperties,
			Description: "an update must be a PatchObject (RFC 8620 §5.3)"}
	}

	upd := &mailboxUpdate{}
	var badProps []string

	for key, val := range patch {
		property, _, hasSub, ok := splitPatchPointer(key)
		if !ok || hasSub {
			return nil, &setError{Type: setErrInvalidPatch,
				Description: "the pointer " + key + " is deeper than any settable Mailbox property; " +
					"no Mailbox property has nested structure (RFC 8621 §2)"}
		}

		switch property {
		case "name":
			var name string
			if err := json.Unmarshal(val, &name); err != nil {
				badProps = append(badProps, "name")
				continue
			}
			upd.name, upd.sawName = name, true

		case "parentId":
			if strings.TrimSpace(string(val)) == "null" {
				upd.parentID, upd.sawParent = 0, true
				continue
			}
			var wire string
			if err := json.Unmarshal(val, &wire); err != nil {
				badProps = append(badProps, "parentId")
				continue
			}
			id, err := DecodeMailboxID(wire)
			if err != nil {
				badProps = append(badProps, "parentId")
				continue
			}
			upd.parentID, upd.sawParent = id, true

		case "isSubscribed":
			var sub bool
			if err := json.Unmarshal(val, &sub); err != nil {
				badProps = append(badProps, "isSubscribed")
				continue
			}
			upd.isSubscribed, upd.sawSubscribed = sub, true

		case "role":
			return nil, &setError{Type: setErrInvalidProperties, Properties: []string{"role"},
				Description: "role is immutable and server-set (RFC 8621 §2)"}

		default:
			badProps = append(badProps, property)
		}
	}

	if len(badProps) > 0 {
		sort.Strings(badProps)
		return nil, &setError{Type: setErrInvalidProperties, Properties: badProps,
			Description: "only name, parentId and isSubscribed may be set on a Mailbox (RFC 8621 §2)"}
	}
	return upd, nil
}

// ---------------------------------------------------------------------------
// destroy
// ---------------------------------------------------------------------------

// applyMailboxDestroy destroys one mailbox, per §2.5 plus the two deviations
// documented at the top of this file.
func (d *Deps) applyMailboxDestroy(ctx context.Context, accountID int64, tree *mailboxTree, wire string, removeEmails bool) *setError {
	id, err := DecodeMailboxID(wire)
	if err != nil {
		return &setError{Type: setErrNotFound}
	}
	row, ok := tree.byID[id]
	if !ok {
		return &setError{Type: setErrNotFound}
	}

	// §2.5: "The server MAY reject the destruction of the Mailbox if it has a
	// role". This server does, for the roles an account cannot function
	// without.
	if isProtectedRole(row.Role) {
		return &setError{Type: setErrForbidden,
			Description: fmt.Sprintf("the %s mailbox is a role folder this account depends on and cannot be destroyed "+
				"(RFC 8621 §2.5 permits this refusal); myRights reports mayDelete:false for it", row.Role)}
	}

	// §2.5 mailboxHasChild, checked before anything is moved: destroying a
	// parent would take its children with it, and the RFC is explicit that the
	// client must remove them first.
	if kids := tree.descendants(id); len(kids) > 0 {
		return &setError{Type: setErrMailboxHasChild,
			Description: fmt.Sprintf("the mailbox has %d child mailbox(es); destroy them first (RFC 8621 §2.5)", len(kids))}
	}

	if row.TotalEmails > 0 {
		if !removeEmails {
			// §2.5, the false branch, implemented exactly.
			return &setError{Type: setErrMailboxHasEmail,
				Description: fmt.Sprintf("the mailbox still holds %d message(s); "+
					"pass onDestroyRemoveEmails:true to move them to Trash and destroy the mailbox", row.TotalEmails)}
		}
		// The true branch, with THE deviation: to Trash, not to oblivion. See
		// the file header for the full argument. Reusing Email/set's destroy
		// (W-A2) rather than a second code path is what makes the two
		// semantics provably identical.
		if serr := d.emptyMailboxToTrash(ctx, accountID, id); serr != nil {
			return serr
		}
	}

	if err := d.Mailboxer.DestroyMailbox(ctx, accountID, id); err != nil {
		return mailboxSetError(err, "destroying the mailbox")
	}
	return nil
}

// emptyMailboxToTrash moves every live message of a mailbox into Trash, using
// the SAME per-message destroy Email/set uses (W-A2).
//
// It is deliberately not a bulk IMAP MOVE of the whole folder: reusing the
// message path means the store reflection, the echo-safety reasoning and the
// Trash resolution are the ones already proven in W1, and a folder-sized MOVE
// would need every one of those arguments made again.
func (d *Deps) emptyMailboxToTrash(ctx context.Context, accountID, mailboxID int64) *setError {
	// Messages already handled, so a pass that re-lists one — because the
	// reader's window did not shrink, or because a concurrent write put it
	// back — cannot make this loop forever. Termination is a property of THIS
	// set, not of the reader's behavior, which is what makes it a guarantee
	// rather than a hope.
	done := map[int64]bool{}

	for pass := range maxEmptyMailboxPasses {
		ids, err := d.Search.SearchEmails(ctx, accountID,
			searchFilter{mailboxID: &mailboxID}, sortSpec{})
		if err != nil {
			return &setError{Type: setErrServerFail,
				Description: "listing the mailbox's messages failed"}
		}

		progressed := false
		for _, msgID := range ids {
			if done[msgID] {
				continue
			}
			done[msgID] = true
			progressed = true

			if err := d.Writer.Destroy(ctx, accountID, msgID); err != nil {
				if errors.Is(err, ErrNotFound) {
					// Expunged by someone else between the listing and now.
					// Nothing to move, nothing wrong.
					continue
				}
				serr := writeSetError(err, "moving the mailbox's messages to Trash")
				serr.Description = "the mailbox could not be emptied: " + serr.Description
				return serr
			}
		}
		if !progressed {
			// Nothing new in this pass: either the folder is empty or every id
			// it still reports has already been moved. Either way the work is
			// done, and the executor's own emptiness check below is the
			// authority on whether the DELETE may proceed.
			return nil
		}
		_ = pass
	}

	// A folder deeper than maxEmptyMailboxPasses windows. Refusing beats
	// looping: the folder is still there, partially emptied, and the client is
	// told plainly to try again.
	return &setError{Type: setErrServerFail,
		Description: fmt.Sprintf("the mailbox still holds messages after %d passes; "+
			"empty it in smaller batches and retry", maxEmptyMailboxPasses)}
}

// maxEmptyMailboxPasses bounds the emptying loop. Each pass moves up to the
// search window's worth of messages, so this covers folders far larger than
// any a user destroys by hand while still terminating.
const maxEmptyMailboxPasses = 64

// ---------------------------------------------------------------------------
// error mapping
// ---------------------------------------------------------------------------

// mailboxSetError maps a MailboxWriter error onto the §5.3 / §2.5 SetError
// vocabulary. Internal error text never reaches the wire, for the same reason
// serverFail keeps it out.
func mailboxSetError(err error, what string) *setError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return &setError{Type: setErrNotFound}
	case errors.Is(err, ErrMailboxExists):
		return &setError{Type: setErrInvalidProperties, Properties: []string{"name"},
			Description: "a mailbox with that name already exists on the server"}
	case errors.Is(err, ErrInvalidName):
		return &setError{Type: setErrInvalidProperties, Properties: []string{"name"},
			Description: "the server refused that mailbox name"}
	case errors.Is(err, ErrMailboxProtected):
		return &setError{Type: setErrForbidden,
			Description: "IMAP forbids this operation on that mailbox (RFC 3501 §6.3.4, §6.3.5)"}
	case errors.Is(err, ErrMailboxHasChild):
		return &setError{Type: setErrMailboxHasChild,
			Description: "the mailbox has child mailboxes; destroy them first (RFC 8621 §2.5)"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &setError{Type: setErrServerUnavailable,
			Description: what + ": request canceled or timed out; retry"}
	default:
		return &setError{Type: setErrServerFail, Description: what + " failed"}
	}
}
