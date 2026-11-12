package limit

import "time"

// RouterReload is the router's reload: it is tried again for this long.
func RouterReload() time.Duration { return 0 }

// RouterPodman is one podman call of the router.
func RouterPodman() time.Duration { return 0 }
