package jmaphttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// The EventSource push endpoint — RFC 8620 §7.3, arbitration W-A4.
//
// §7.3: "Clients that can hold transport connections open can connect
// directly to the JMAP server to receive push notifications via a
// 'text/event-stream' resource, as described in [EventSource]. This is a long
// running HTTP request, where the server can push data to the client by
// appending data without ending the response."
//
// # The division of labor with the broker
//
// internal/sync's Broker knows WHEN an account changed. This file knows WHAT
// to say about it: §7.1 defines a TypeState value as "the 'state' property
// that would currently be returned by a call to 'Foo/get'", so the payload is
// read at write time from the SAME StateReader that serves Email/get,
// Mailbox/get and Thread/get. That is the whole reason the broker publishes
// an account id rather than a payload — see internal/sync/broker.go.
//
// A client that receives a state string here and immediately calls
// Email/changes with it therefore gets an exact match, which is the property
// §7.1 depends on ("The client can compare the new state strings with its
// current values to see whether it has the current data for these types").
//
// # Last-Event-ID, honestly
//
// §7.3 SHOULDs an event id "that encodes the entire server state visible to
// the user", so that on reconnection "the server can use [Last-Event-ID] to
// work out whether the client has missed some changes. If so, it SHOULD send
// these changes immediately on connection."
//
// This server satisfies that by construction rather than by keeping a
// replay log, and the reasoning is worth stating because it looks like a
// shortcut and is not: the id emitted here IS the encoded state snapshot (the
// same string set the event carries). A §7.1 payload is a snapshot, not a
// diff, so "the changes the client missed" is always expressible as one
// current-state event — replaying a history would deliver nothing a single
// fresh snapshot does not already imply.
//
// So: every connection sends one state event immediately (§7.3's "send these
// changes immediately on connection"), and a client that presents a
// Last-Event-ID matching the current state learns that nothing was missed
// from the identical id it receives back. What this deliberately does NOT do
// is pretend to distinguish "you missed three changes" from "you missed one";
// no client can act differently on that knowledge, because the remedy —
// call /changes with your cursor — is the same.

// Defaults and bounds for the EventSource endpoint.
const (
	// DefaultPingInterval is the ping interval used when the client asks for
	// one without naming a value it can get.
	DefaultPingInterval = 30 * time.Second

	// MinPingInterval and MaxPingInterval bound what a client may request.
	//
	// §7.3: "The server MAY modify a requested ping interval to be subject to
	// a minimum and/or maximum value. For interoperability, servers MUST NOT
	// have a minimum allowed value higher than 30 or a maximum allowed value
	// less than 300." These are exactly the extreme values the RFC permits: a
	// minimum any higher, or a maximum any lower, would be non-conforming.
	MinPingInterval = 30 * time.Second
	MaxPingInterval = 300 * time.Second

	// DefaultMaxSSEPerAccount is the per-account connection cap (W-A4:
	// "Límite de conexiones SSE por cuenta (config, default 4)").
	DefaultMaxSSEPerAccount = 4
)

// StateSource resolves the current per-type state strings for one account.
//
// It is a local interface for the same reason BlobReader is: the transport
// needs exactly one thing from the mail layer, and declaring it here keeps a
// protocol package free of a hard dependency. mail.StateReader satisfies it
// by construction — which is the point, because satisfying it with the SAME
// implementation that answers /get and /changes is what makes the pushed
// strings comparable rather than merely plausible.
type StateSource interface {
	MailboxState(ctx context.Context, accountID int64) (string, error)
	EmailState(ctx context.Context, accountID int64) (string, error)
	ThreadState(ctx context.Context, accountID int64) (string, error)
}

// StateNotifier is the transport's view of internal/sync's Broker: subscribe
// to one account's change notifications, and cancel when done.
//
// The element type is Notification — an empty struct — rather than the
// broker's own event type, because the transport genuinely reads nothing out
// of it: a notification means "this account changed, go read its states", and
// the states come from StateSource at send time (see the file comment). A
// wider element type here would invite a future reader to push a state string
// through this channel, which is the one thing that must not happen.
//
// internal/sync's Broker does not satisfy this directly; cmd/moovd adapts it
// in a few lines. That is deliberate: the adapter is where the "ignore the
// payload" decision is visible, instead of being buried in a type assertion.
type StateNotifier interface {
	// StateEvents returns a channel that yields a value whenever the account's
	// data changed, and a cancel function that must be called to release the
	// subscription. The channel is closed when the broker shuts down.
	StateEvents(accountID int64) (<-chan Notification, func())

	// Subscribers reports the account's current live subscription count, for
	// the connection cap.
	Subscribers(accountID int64) int
}

// Notification is one "something changed" signal. It carries nothing on
// purpose.
type Notification struct{}

// SSERecorder is the metrics layer's view of the push endpoint. One gauge
// delta and one counter; see internal/metrics.
type SSERecorder interface {
	// AddSSEConnections adjusts the live-connection gauge by delta.
	AddSSEConnections(delta float64)
	// IncSSEEvents counts one emitted event, by kind ("state" or "ping").
	IncSSEEvents(kind string)
}

// handleEventSource serves GET /jmap/eventsource (RFC 8620 §7.3).
func (s *Server) handleEventSource(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromContext(r.Context())
	if !ok {
		writeGenericProblem(w, http.StatusInternalServerError, "authentication context missing")
		return
	}
	if s.notifier == nil || s.state == nil {
		// Not wired (a server built for protocol tests, or a deployment with
		// no sync engine). Saying 501 is the honest answer and is what the
		// route said before W4a implemented it.
		writeGenericProblem(w, http.StatusNotImplemented,
			"event source push is not available on this server")
		return
	}

	opts, perr := parseEventSourceQuery(r.URL.Query())
	if perr != "" {
		// A malformed query parameter is a plain bad request, not one of the
		// §3.6.1 problem types: those are defined for the API endpoint's
		// request object ("using", the JSON body, the request limits), and
		// none of them describes "your closeafter value is not a value this
		// RFC defines". A generic 400 problem says exactly what happened.
		writeGenericProblem(w, http.StatusBadRequest, perr)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		// Every net/http response writer flushes; a middleware that wrapped it
		// without passing Flush through would break streaming silently, so this
		// fails loudly instead of serving a stream nobody ever receives.
		// (statusRecorder in middleware.go implements Flush for exactly this.)
		writeGenericProblem(w, http.StatusInternalServerError,
			"the response writer does not support streaming")
		return
	}

	// The cap (W-A4). Enforced BEFORE subscribing so the count cannot include
	// this connection, and answered with 429 rather than by dropping an older
	// stream: §7.3 says "A client MAY hold open multiple connections to the
	// event-source resource, although it SHOULD try to use a single connection
	// for efficiency" — a client that exceeds the limit is doing something the
	// RFC discourages, and telling it so is more useful than silently killing
	// a connection another tab is using correctly. Retry-After makes the
	// refusal actionable.
	if n := s.notifier.Subscribers(id.Account.ID); n >= s.maxSSEPerAccount {
		w.Header().Set("Retry-After", "10")
		writeRequestError(w, jmap.NewLimitError(http.StatusTooManyRequests,
			"maxConcurrentEventSource",
			fmt.Sprintf("this account already holds %d event-source connections", n)))
		return
	}

	events, cancel := s.notifier.StateEvents(id.Account.ID)
	defer cancel()

	// §7.3 requires the text/event-stream media type. The rest of these
	// headers are what keeps a stream alive across the real deployment's
	// fronting Caddy and any intermediary: no caching of a stream that is
	// never complete, and no proxy buffering (X-Accel-Buffering is honored by
	// nginx and Caddy alike) — buffering is precisely the failure §7.3's
	// closeafter=state mode exists to work around, and not provoking it is
	// better than relying on the workaround.
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	// Other people's mail state is never a shared cache's business, and a
	// sniffing intermediary must not reinterpret the stream.
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if s.metrics != nil {
		if rec, ok := s.metrics.(SSERecorder); ok {
			rec.AddSSEConnections(1)
			defer rec.AddSSEConnections(-1)
		}
	}

	s.streamEvents(r, w, flusher, id, opts, events)
}

// streamEvents is the endpoint's loop: an immediate state event, then one per
// notification, with pings in between, until the client leaves or the server
// shuts down.
func (s *Server) streamEvents(
	r *http.Request,
	w http.ResponseWriter,
	flusher http.Flusher,
	id *Identity,
	opts eventSourceOptions,
	events <-chan Notification,
) {
	ctx := r.Context()

	// §7.3: on a new connection the server "SHOULD send these changes
	// immediately on connection" when Last-Event-ID indicates the client may
	// have missed something. This server sends the current state
	// unconditionally, which satisfies that for every client — including one
	// connecting for the first time, which has no cursor at all and would
	// otherwise wait for the next change to learn where it stands.
	//
	// The one case where the RFC's wording permits saying nothing is a
	// reconnect whose Last-Event-ID equals the current state. Sending it
	// anyway costs one small frame and removes a class of bug (a client that
	// never receives a first event cannot tell a healthy idle stream from a
	// broken one), so it is sent, and the identical id tells the client
	// nothing changed.
	if lastSeen := r.Header.Get("Last-Event-ID"); lastSeen != "" {
		s.log.Debug("jmap: event-source client resumed",
			"account_id", id.Account.ID, "last_event_id", lastSeen)
	}
	sent, err := s.writeStateEvent(ctx, w, flusher, id, opts)
	if err != nil {
		return
	}
	if opts.closeAfterState && sent {
		// §7.3, closeafter=state: "The server MUST end the HTTP response
		// after pushing a state event." Returning ends the handler, which
		// ends the response.
		return
	}

	// The ping timer measures idleness, not wall-clock periods: §7.3 says the
	// server "MUST send an event called 'ping' whenever this time elapses
	// since THE PREVIOUS EVENT was sent" (emphasis on which instant restarts
	// it). So it is a timer reset after every event of any kind, not a
	// free-running ticker — a ticker would fire pings on a busy stream that
	// has just sent data, which is what the RFC's phrasing rules out.
	var pingC <-chan time.Time
	var pingTimer *time.Timer
	if opts.pingInterval > 0 {
		pingTimer = time.NewTimer(opts.pingInterval)
		defer pingTimer.Stop()
		pingC = pingTimer.C
	}
	resetPing := func() {
		if pingTimer == nil {
			return
		}
		if !pingTimer.Stop() {
			// Drain a timer that fired while we were writing, so the reset
			// interval is measured from this event rather than firing
			// immediately after it.
			select {
			case <-pingTimer.C:
			default:
			}
		}
		pingTimer.Reset(opts.pingInterval)
	}

	for {
		select {
		case <-ctx.Done():
			// The client went away (or the server is shutting down and
			// Shutdown canceled the request context). Nothing to write.
			return

		case _, open := <-events:
			if !open {
				// The broker closed: clean shutdown (W-A4: "cierre limpio en
				// shutdown"). Ending the handler ends the response properly,
				// so the client sees a finished stream and reconnects per the
				// EventSource spec, rather than a truncated one.
				return
			}
			sent, err := s.writeStateEvent(ctx, w, flusher, id, opts)
			if err != nil {
				return
			}
			if sent {
				resetPing()
				if opts.closeAfterState {
					return
				}
			}

		case <-pingC:
			// §7.3: "The data for the ping event MUST be a JSON object
			// containing an 'interval' property, the value (type UnsignedInt)
			// being the interval in seconds the server is using to send pings
			// (this may be different to the requested value if the server
			// clamped it to be within a min/max value)."
			//
			// Note what is NOT here: an id. "This MUST NOT set a new event
			// id" — a ping must not move the client's Last-Event-ID, or a
			// reconnect would resume from a cursor that never corresponded to
			// a state.
			payload := fmt.Sprintf(`{"interval":%d}`, int(opts.pingInterval/time.Second))
			if err := writeSSE(w, flusher, "ping", "", payload); err != nil {
				return
			}
			if s.metrics != nil {
				if rec, ok := s.metrics.(SSERecorder); ok {
					rec.IncSSEEvents("ping")
				}
			}
			resetPing()
		}
	}
}

// writeStateEvent reads the account's current states and emits one §7.1
// StateChange as an SSE "state" event. It reports whether an event was
// actually written.
func (s *Server) writeStateEvent(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	id *Identity,
	opts eventSourceOptions,
) (bool, error) {
	states, err := s.readStates(ctx, id.Account.ID, opts.types)
	if err != nil {
		// A state read that fails is not a reason to kill a healthy stream:
		// the next notification retries, and the client is no worse off than
		// during any other momentary store hiccup. Logged, not fatal.
		s.log.Warn("jmap: reading push state failed",
			"account_id", id.Account.ID, "error", err)
		return false, nil
	}
	if len(states) == 0 {
		// Every requested type was filtered out (a "types" list naming only
		// types this server does not push). §7.3: "The server MUST only push
		// changes for the types in this list" — so it pushes nothing, rather
		// than an empty object that would claim a change occurred.
		return false, nil
	}

	// The event id encodes the whole snapshot, which is what makes
	// Last-Event-ID meaningful (see the file comment).
	eventID := stateEventID(states)

	change := map[string]any{
		// §7.1: "@type: 'String' — This MUST be the string 'StateChange'."
		"@type": "StateChange",
		// §7.1: "changed: 'Id[TypeState]' — A map of an 'account id' to an
		// object encoding the state of data types that have changed for that
		// account". One account: Basic auth authenticates exactly one mailbox
		// owner and the session advertises exactly one account.
		"changed": map[string]any{
			id.AccountID: states,
		},
	}
	body, err := json.Marshal(change)
	if err != nil {
		s.log.Error("jmap: marshaling StateChange failed",
			"account_id", id.Account.ID, "error", err)
		return false, nil
	}

	if err := writeSSE(w, flusher, "state", eventID, string(body)); err != nil {
		return false, err
	}
	if s.metrics != nil {
		if rec, ok := s.metrics.(SSERecorder); ok {
			rec.IncSSEEvents("state")
		}
	}
	return true, nil
}

// readStates resolves the state strings for the requested types.
//
// The type names are RFC 8621's: "Mailbox", "Email", "Thread". A requested
// type this server does not implement is silently absent rather than an
// error — §7.3 constrains what the server may PUSH ("MUST only push changes
// for the types in this list"), and a client asking for CalendarEvent from a
// mail-only server is asking a coherent question with the answer "never".
func (s *Server) readStates(ctx context.Context, accountID int64, types typeFilter) (map[string]string, error) {
	out := make(map[string]string, 3)

	if types.wants("Mailbox") {
		st, err := s.state.MailboxState(ctx, accountID)
		if err != nil {
			return nil, fmt.Errorf("mailbox state: %w", err)
		}
		out["Mailbox"] = st
	}
	if types.wants("Email") {
		st, err := s.state.EmailState(ctx, accountID)
		if err != nil {
			return nil, fmt.Errorf("email state: %w", err)
		}
		out["Email"] = st
	}
	if types.wants("Thread") {
		st, err := s.state.ThreadState(ctx, accountID)
		if err != nil {
			return nil, fmt.Errorf("thread state: %w", err)
		}
		out["Thread"] = st
	}
	return out, nil
}

// stateEventID encodes a whole state snapshot into one event id (§7.3: "a new
// event id that encodes the entire server state visible to the user").
//
// The types are emitted in a fixed order so the same snapshot always produces
// the same id — a map's iteration order would make an id that changes without
// the state changing, which would defeat the "did I miss anything" comparison
// the id exists for.
func stateEventID(states map[string]string) string {
	var b strings.Builder
	for _, t := range []string{"Mailbox", "Email", "Thread"} {
		st, ok := states[t]
		if !ok {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(';')
		}
		b.WriteString(t)
		b.WriteByte(':')
		b.WriteString(st)
	}
	return b.String()
}

// writeSSE writes one server-sent event and flushes it.
//
// The framing is the EventSource wire format the RFC delegates to: optional
// "id:", an "event:" name, one "data:" line per line of payload, terminated
// by a blank line. The payloads here are compact JSON with no newlines, but
// the split is done anyway — a data value containing a newline would
// otherwise silently truncate the event at the reader.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, event, id, data string) error {
	var b strings.Builder
	if id != "" {
		// An id must not contain a newline; the state strings it is built
		// from are digits, dashes and type names, but the guard is cheap and
		// makes the framing unbreakable by construction.
		b.WriteString("id: ")
		b.WriteString(strings.NewReplacer("\n", "", "\r", "").Replace(id))
		b.WriteByte('\n')
	}
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteByte('\n')
	for _, line := range strings.Split(data, "\n") {
		b.WriteString("data: ")
		b.WriteString(strings.TrimSuffix(line, "\r"))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')

	if _, err := w.Write([]byte(b.String())); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// ---------------------------------------------------------------------------
// query parameters (§7.3)
// ---------------------------------------------------------------------------

// eventSourceOptions is the parsed query string.
type eventSourceOptions struct {
	types           typeFilter
	closeAfterState bool
	pingInterval    time.Duration
}

// typeFilter is the parsed "types" variable: nil means "*" (everything).
type typeFilter map[string]struct{}

// wants reports whether a type should be pushed.
func (f typeFilter) wants(name string) bool {
	if f == nil {
		return true
	}
	_, ok := f[name]
	return ok
}

// parseEventSourceQuery parses the three §7.3 variables.
//
// The session advertises the URL as a level-1 URI Template, so a client that
// substitutes nothing sends the literal "{types}" strings. Those are treated
// as "unset" rather than rejected: a template that was never expanded is a
// client bug the endpoint can survive with sane defaults, and refusing it
// would turn a curl-by-hand into an error nobody can read.
func parseEventSourceQuery(q map[string][]string) (eventSourceOptions, string) {
	opts := eventSourceOptions{pingInterval: DefaultPingInterval}

	// "types": §7.3 — "A comma-separated list of type names, e.g.,
	// 'Email,CalendarEvent'. The server MUST only push changes for the types
	// in this list." Or "The single character: '*'. Changes to all types are
	// pushed."
	raw := firstValue(q, "types")
	if raw != "" && !isUnexpandedTemplate(raw) && raw != "*" {
		filter := make(typeFilter)
		for _, name := range strings.Split(raw, ",") {
			if name = strings.TrimSpace(name); name != "" {
				filter[name] = struct{}{}
			}
		}
		if len(filter) == 0 {
			return opts, `the "types" parameter must be "*" or a comma-separated list of type names (RFC 8620 §7.3)`
		}
		opts.types = filter
	}

	// "closeafter": §7.3 — "state" ends the response after one state event,
	// "no" persists the connection. Anything else is not a value the RFC
	// defines, and guessing at it would produce a stream that behaves in a
	// way the client did not ask for.
	switch ca := firstValue(q, "closeafter"); {
	case ca == "" || isUnexpandedTemplate(ca) || ca == "no":
		opts.closeAfterState = false
	case ca == "state":
		opts.closeAfterState = true
	default:
		return opts, `the "closeafter" parameter must be "state" or "no" (RFC 8620 §7.3)`
	}

	// "ping": §7.3 — "A positive integer value representing a length of time
	// in seconds... If the value is '0', the server MUST NOT send ping
	// events."
	if p := firstValue(q, "ping"); p != "" && !isUnexpandedTemplate(p) {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return opts, `the "ping" parameter must be a non-negative integer number of seconds (RFC 8620 §7.3)`
		}
		if n == 0 {
			opts.pingInterval = 0
		} else {
			opts.pingInterval = clampPing(time.Duration(n) * time.Second)
		}
	}
	return opts, ""
}

// clampPing applies the §7.3 min/max the server is allowed to impose.
func clampPing(d time.Duration) time.Duration {
	if d < pingFloor {
		return pingFloor
	}
	if d > MaxPingInterval {
		return MaxPingInterval
	}
	return d
}

// pingFloor is the lower bound clampPing enforces. It is a variable rather
// than a use of the constant so that a test can drive the real timer path in
// milliseconds instead of sleeping through a conforming 30-second interval;
// production never changes it, and TestSSEPingBoundsAreConforming asserts the
// exported constant still satisfies §7.3.
var pingFloor = MinPingInterval

// isUnexpandedTemplate reports a URI Template variable the client forgot to
// substitute, e.g. "{types}".
func isUnexpandedTemplate(v string) bool {
	return strings.HasPrefix(v, "{") && strings.HasSuffix(v, "}")
}

// firstValue returns the first value of a query parameter.
func firstValue(q map[string][]string, key string) string {
	if vs := q[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}
