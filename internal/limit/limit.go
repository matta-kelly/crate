// Package limit is the one home of every limit's value. CRATE_LIMIT_SCALE divides each of them, so a test
// makes a limit pass in seconds. A later cut puts its own limits in a file of its own, by name.
package limit

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const scaleEnv = "CRATE_LIMIT_SCALE"

// CheckScale refuses a CRATE_LIMIT_SCALE that is not a whole number of 1 or more. The entry point calls it
// once, so a limit below can take the scale as read.
func CheckScale() error {
	_, err := scale()
	return err
}

func scale() (int, error) {
	v := os.Getenv(scaleEnv)
	if v == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 1, fmt.Errorf("%s is %q: expected a whole number of 1 or more", scaleEnv, v)
	}
	return n, nil
}

// scaled divides a limit by the factor, but never below 1 s: on a busy machine a call needs that long to be
// met. With no factor, a limit is its value.
func scaled(d time.Duration) time.Duration {
	n, _ := scale()
	// With no factor, a limit is its value, whatever its size: the floor below belongs to a divided limit.
	if n == 1 {
		return d
	}
	return max(d/time.Duration(n), time.Second)
}

func Git() time.Duration           { return scaled(30 * time.Second) }
func SkopeoInspect() time.Duration { return scaled(30 * time.Second) }
func SkopeoCopy() time.Duration    { return scaled(10 * time.Minute) }
func Pull() time.Duration          { return scaled(15 * time.Minute) }
func Push() time.Duration          { return scaled(15 * time.Minute) }
func Build() time.Duration         { return scaled(30 * time.Minute) }
func Smoke() time.Duration         { return scaled(10 * time.Minute) }
func Gh() time.Duration            { return scaled(5 * time.Minute) }

// Podman is a call of the engine that only reads or moves a name: image exists, tag, image rm.
func Podman() time.Duration { return scaled(30 * time.Second) }

// Check is the release's check: go vet, go test and the shell tests.
func Check() time.Duration { return scaled(30 * time.Minute) }

// Hold is the wait for a slot on the machine.
func Hold() time.Duration { return scaled(time.Hour) }

// Up is a person's and an attempt's `crate up`.
func Up() time.Duration { return scaled(15 * time.Minute) }

// AppReady is how long the app has to pass its health command once it is started.
func AppReady() time.Duration { return scaled(time.Minute) }

// Image is the longest `crate dev-image local` may take: Pull + Build + Smoke.
func Image() time.Duration { return Pull() + Build() + Smoke() }
