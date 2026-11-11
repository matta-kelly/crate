// Package limit is the one home of every limit's value. A stub: da-hammer builds it.
package limit

import "time"

// Each limit, divided by CRATE_LIMIT_SCALE (a whole number of 1 or more, 1 when unset).
func Git() time.Duration           { return 0 }
func SkopeoInspect() time.Duration { return 0 }
func SkopeoCopy() time.Duration    { return 0 }
func Pull() time.Duration          { return 0 }
func Push() time.Duration          { return 0 }
func Build() time.Duration         { return 0 }
func Smoke() time.Duration         { return 0 }
func Gh() time.Duration            { return 0 }
func Hold() time.Duration          { return 0 }
func Up() time.Duration            { return 0 }

// Image is the longest `crate dev-image local` may take: Pull + Build + Smoke.
func Image() time.Duration { return 0 }
