package limit

import "time"

// SlotLock is the short lock of a kind's folder, held while a slot record is written.
func SlotLock() time.Duration { return 0 }

// Systemd is the limit of each systemctl and systemd-run call.
func Systemd() time.Duration { return 0 }
