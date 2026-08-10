package imap

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
)

// captureCommand runs issue against a client connected to a minimal fake
// server, and returns the first command line the client sent after the
// greeting exchange.
//
// It exists because the assertions that matter most in this package are about
// bytes on the wire — whether NOTIFY is spelled the way Dovecot accepts. A
// mock at the library's API level would assert nothing: the bugs patch 0002
// fixes live in the encoder, below any API a mock could stand in for.
//
// The fake server answers only what is needed to get the client into the
// authenticated state, then records. It is not an IMAP server and does not try
// to be; go-imap's imapmemserver is used where a real server is needed, but it
// has no NOTIFY support (S2), which is exactly the feature under test here.
func captureCommand(issue func(*imapclient.Client)) (string, error) {
	clientConn, serverConn := net.Pipe()

	var (
		mu       sync.Mutex
		captured string
		srvErr   error
	)
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer func() { _ = serverConn.Close() }()

		// The pipe is synchronous and unbuffered, so every read and write
		// needs a deadline or a mistake becomes a hang instead of a failure.
		_ = serverConn.SetDeadline(time.Now().Add(5 * time.Second))

		br := bufio.NewReader(serverConn)
		bw := bufio.NewWriter(serverConn)

		writeLine := func(s string) error {
			if _, err := bw.WriteString(s + "\r\n"); err != nil {
				return err
			}
			return bw.Flush()
		}

		// Greeting, with the capabilities the client checks at connect time.
		if err := writeLine("* OK [CAPABILITY IMAP4rev1 CONDSTORE QRESYNC IDLE NOTIFY METADATA] fake"); err != nil {
			mu.Lock()
			srvErr = err
			mu.Unlock()
			return
		}

		for {
			line, err := br.ReadString('\n')
			if err != nil {
				if !errors.Is(err, net.ErrClosed) && err.Error() != "io: read/write on closed pipe" {
					mu.Lock()
					if srvErr == nil {
						srvErr = err
					}
					mu.Unlock()
				}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				continue
			}

			tag, rest, _ := strings.Cut(line, " ")
			verb, _, _ := strings.Cut(rest, " ")

			switch strings.ToUpper(verb) {
			case "CAPABILITY":
				_ = writeLine("* CAPABILITY IMAP4rev1 CONDSTORE QRESYNC IDLE NOTIFY METADATA")
				_ = writeLine(tag + " OK CAPABILITY done")
			case "NOOP":
				_ = writeLine(tag + " OK NOOP done")
			default:
				// The command under test. Record it and stop answering: the
				// caller only needs the bytes.
				mu.Lock()
				if captured == "" {
					captured = line
				}
				mu.Unlock()
				return
			}
		}
	}()

	client := imapclient.New(clientConn, &imapclient.Options{})
	if err := client.WaitGreeting(); err != nil {
		_ = clientConn.Close()
		<-done
		return "", fmt.Errorf("waiting for greeting: %w", err)
	}

	issue(client)

	// Closing the client's side unblocks the fake server's read.
	_ = clientConn.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if captured == "" {
		if srvErr != nil {
			return "", fmt.Errorf("no command captured: %w", srvErr)
		}
		return "", errors.New("no command captured")
	}
	return captured, nil
}
