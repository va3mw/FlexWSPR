// discover lists FlexRadios seen on the LAN for a few seconds.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/va3mw/FlexWSPR/internal/flex"
)

func main() {
	seen := map[string]bool{}
	end := time.Now().Add(6 * time.Second)
	for time.Now().Before(end) {
		ctx, cancel := context.WithDeadline(context.Background(), end)
		info, err := flex.Discover(ctx, "")
		cancel()
		if err != nil {
			break
		}
		if !seen[info.Serial] {
			seen[info.Serial] = true
			fmt.Printf("%s %q serial %s at %s v%s status %s\n", info.Model, info.Nickname, info.Serial, info.IP, info.Version, info.Status)
		}
	}
	if len(seen) == 0 {
		fmt.Println("no radios seen")
	}
}
