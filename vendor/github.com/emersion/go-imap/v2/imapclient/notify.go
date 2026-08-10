package imapclient

import (
	"fmt"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/internal/imapwire"
)

// Notify sends a NOTIFY command (RFC 5465).
//
// The NOTIFY command allows clients to request server-push notifications
// for mailbox events like new messages, expunges, flag changes, etc.
//
// When NOTIFY SET is active, the server may send unsolicited responses at any
// time (STATUS, FETCH, EXPUNGE, LIST responses). These unsolicited responses
// are delivered via the UnilateralDataHandler callbacks set in
// imapclient.Options.
//
// When the server sends NOTIFICATIONOVERFLOW, the NotificationOverflow callback
// in UnilateralDataHandler will be called (if set).
func (c *Client) Notify(options *imap.NotifyOptions) (*NotifyCommand, error) {
	cmd := &NotifyCommand{}
	enc := c.beginCommand("NOTIFY", cmd)
	if err := encodeNotifyOptions(enc.Encoder, options); err != nil {
		enc.end()
		return nil, err
	}
	enc.end()

	return cmd, nil
}

// encodeNotifyOptions encodes NOTIFY command options to the encoder.
func encodeNotifyOptions(enc *imapwire.Encoder, options *imap.NotifyOptions) error {
	if options == nil || len(options.Items) == 0 {
		// NOTIFY NONE: disable all notifications.
		enc.SP().Atom("NONE")
		return nil
	}

	enc.SP().Atom("SET")

	// RFC 5465 section 6:
	//
	//	notify-set = "SET" [SP "STATUS"] 1*(SP event-group)
	//
	// The STATUS indicator is a bare atom, not a parenthesised list. Emitting
	// "(STATUS)" makes Dovecot reject the whole command with
	// "BAD Error in IMAP command NOTIFY: Invalid arguments".
	if options.Status {
		enc.SP().Atom("STATUS")
	}

	for _, item := range options.Items {
		if item.MailboxSpec == "" && len(item.Mailboxes) == 0 {
			return fmt.Errorf("invalid NOTIFY item: must specify either MailboxSpec or Mailboxes")
		}

		enc.SP().List(1, func(_ int) {
			if item.MailboxSpec != "" {
				enc.Atom(string(item.MailboxSpec))
			} else {
				// len(item.Mailboxes) > 0, as per the check above.
				//
				// RFC 5465 section 6:
				//
				//	filter-mailboxes-other = ("SUBTREE" / "MAILBOXES")
				//	                         SP one-or-more-mailbox
				//
				// The selector keyword is mandatory in both branches: an
				// explicit mailbox list must be introduced by MAILBOXES.
				// Without it Dovecot rejects the command with
				// "BAD Error in IMAP command NOTIFY: Invalid arguments".
				if item.Subtree {
					enc.Atom("SUBTREE").SP()
				} else {
					enc.Atom("MAILBOXES").SP()
				}
				enc.List(len(item.Mailboxes), func(j int) {
					enc.Mailbox(item.Mailboxes[j])
				})
			}

			if len(item.Events) > 0 {
				enc.SP().List(len(item.Events), func(j int) {
					enc.Atom(string(item.Events[j]))
				})
			}
		})

	}

	return nil
}

// NotifyCommand is a NOTIFY command.
//
// When NOTIFY SET is active, the server may send unsolicited responses at any
// time. These responses are delivered via UnilateralDataHandler
// (see Options.UnilateralDataHandler).
//
// If the server sends NOTIFICATIONOVERFLOW, the NotificationOverflow callback
// in UnilateralDataHandler will be called (if set).
type NotifyCommand struct {
	commandBase
}

// Wait blocks until the NOTIFY command has completed.
func (cmd *NotifyCommand) Wait() error {
	return cmd.wait()
}
