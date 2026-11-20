package record

import (
	"net"
	"sync"
)

// Journal is whether each line of the record also goes to the system journal, tagged crate, so the node's log
// shipper carries the server's record with the rest of its logs. The server's verbs set it; a person's run leaves it
// off.
var Journal bool

// journalSocket is journald's native socket (sd_journal_send(3)); a test points it elsewhere.
var journalSocket = "/run/systemd/journal/socket"

var journalMu sync.Mutex

// toJournal sends one line of the record as one journal entry: SYSLOG_IDENTIFIER=crate, and the JSON line as its
// MESSAGE, one datagram in journald's native protocol (systemd.journal-fields(7)). A line it cannot send is dropped,
// as the record never fails a run.
func toJournal(line []byte) {
	journalMu.Lock()
	defer journalMu.Unlock()
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: journalSocket, Net: "unixgram"})
	if err != nil {
		return
	}
	defer conn.Close()
	msg := append([]byte("SYSLOG_IDENTIFIER=crate\nPRIORITY=6\nMESSAGE="), line...)
	_, _ = conn.Write(append(msg, '\n'))
}
