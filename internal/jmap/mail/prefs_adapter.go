package mail

import (
	"context"
	"errors"

	"github.com/GrupoNU/moov/internal/store"
)

// The store-backed PrefsStore: the only file in the preference surface that
// knows a Prefs object is a row in account_prefs — the same confinement
// identity_adapter.go and submission_adapter.go give their types.

// PrefsAdapter implements PrefsStore over the real store.
type PrefsAdapter struct {
	store    *store.Store
	notifier SubmissionNotifier
}

// NewPrefsAdapter builds the adapter.
//
// notifier may be nil. When set it is the same *sync.Broker the submission and
// identity adapters hold, so saving a preference pushes an SSE StateChange and
// the user's OTHER sessions pick it up without a reload. That matters more for
// preferences than for most types: a user who switches to dark theme on the
// laptop expects the phone's open tab to follow, and the reconciliation the
// PWA does against its pre-paint localStorage cache is driven by exactly this
// push.
func NewPrefsAdapter(st *store.Store, notifier SubmissionNotifier) (*PrefsAdapter, error) {
	if st == nil {
		return nil, errors.New("mail: a store is required")
	}
	return &PrefsAdapter{store: st, notifier: notifier}, nil
}

var _ PrefsStore = (*PrefsAdapter)(nil)

// GetPrefs implements PrefsStore.
func (a *PrefsAdapter) GetPrefs(ctx context.Context, accountID int64) (PrefsRecord, error) {
	rec, err := a.store.GetPrefs(ctx, accountID)
	if err != nil {
		return PrefsRecord{}, err
	}
	return prefsRecord(rec), nil
}

// PutPrefs implements PrefsStore.
func (a *PrefsAdapter) PutPrefs(ctx context.Context, accountID int64, p PrefsValue) (PrefsRecord, error) {
	rec, err := a.store.PutPrefs(ctx, accountID, storePrefs(p))
	if err != nil {
		return PrefsRecord{}, err
	}
	if a.notifier != nil {
		a.notifier.Notify(accountID)
	}
	return prefsRecord(rec), nil
}

// PrefsState implements PrefsStore — the same watermark-and-count grammar
// every other type's state uses (adapter.go stateFor).
func (a *PrefsAdapter) PrefsState(ctx context.Context, accountID int64) (string, error) {
	return prefsStateString(ctx, a.store, accountID)
}

// prefsStateString is the Prefs type's state.
func prefsStateString(ctx context.Context, st *store.Store, accountID int64) (string, error) {
	watermark, err := st.PrefsWatermark(ctx, accountID)
	if err != nil {
		return "", err
	}
	count, err := st.CountPrefs(ctx, accountID)
	if err != nil {
		return "", err
	}
	return stateFor(watermark, count), nil
}

// PrefsState exposes the preference state on the SAME reader every other
// type's state comes from (deps.State, which jmaphttp's EventSource consults),
// so a pushed Prefs state string equals the one Prefs/get returns.
func (a *Adapter) PrefsState(ctx context.Context, accountID int64) (string, error) {
	return prefsStateString(ctx, a.store, accountID)
}

// ---------------------------------------------------------------------------
// value mapping
// ---------------------------------------------------------------------------

// prefsRecord and storePrefs are the whole translation between the store's
// shape and this package's, and they are written out field by field rather
// than by embedding or reflection ON PURPOSE: adding a preference must fail to
// compile in both directions until it is mapped, which is what
// TestPrefsMappingCoversEveryField checks cannot be quietly satisfied by a
// struct copy.

func prefsRecord(r store.PrefsRecord) PrefsRecord {
	return PrefsRecord{
		Prefs:     prefsValue(r.Prefs),
		UpdatedAt: r.UpdatedAt,
		Exists:    r.Exists,
	}
}

func prefsValue(p store.Prefs) PrefsValue {
	return PrefsValue{
		UndoSendSeconds:   p.UndoSendSeconds,
		ImagesPolicy:      p.ImagesPolicy,
		ConversationView:  p.ConversationView,
		HoverActions:      p.HoverActions,
		AutoAdvance:       p.AutoAdvance,
		Density:           p.Density,
		ShowSnippets:      p.ShowSnippets,
		KeyboardShortcuts: p.KeyboardShortcuts,
		Language:          p.Language,
		ReadingPane:       p.ReadingPane,
		InboxType:         p.InboxType,
		Notifications:     p.Notifications,
		Theme:             p.Theme,
	}
}

func storePrefs(p PrefsValue) store.Prefs {
	return store.Prefs{
		UndoSendSeconds:   p.UndoSendSeconds,
		ImagesPolicy:      p.ImagesPolicy,
		ConversationView:  p.ConversationView,
		HoverActions:      p.HoverActions,
		AutoAdvance:       p.AutoAdvance,
		Density:           p.Density,
		ShowSnippets:      p.ShowSnippets,
		KeyboardShortcuts: p.KeyboardShortcuts,
		Language:          p.Language,
		ReadingPane:       p.ReadingPane,
		InboxType:         p.InboxType,
		Notifications:     p.Notifications,
		Theme:             p.Theme,
	}
}

// DefaultPrefsValue is the product's factory setting, in this package's shape.
// It is the store's DefaultPrefs mapped across, so there is exactly one place
// a default is written down.
func DefaultPrefsValue() PrefsValue { return prefsValue(store.DefaultPrefs()) }

// PrefsSchemaVersion is the preference schema version this server speaks,
// re-exported for the session object's vendor capability.
//
// It is re-exported rather than read from internal/store by the transport
// because the session is assembled entirely from this package's view of the
// mail surface, and routing one integer around that boundary would be the only
// place jmaphttp reached into storage for a PROTOCOL value. The constant has
// exactly one definition (store.PrefsSchemaVersion); this is a name for it.
const PrefsSchemaVersion = store.PrefsSchemaVersion
