package limit

import "time"

// SlotLock is the short lock of a kind's folder, held while a slot record is written.
func SlotLock() time.Duration { return scaled(10 * time.Second) }

// Systemd is the limit of each systemctl and systemd-run call.
func Systemd() time.Duration { return scaled(30 * time.Second) }
