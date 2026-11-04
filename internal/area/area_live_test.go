package area

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// Run with FLEXWSPR_LIVE=1 to query wspr.live.
func TestLive(t *testing.T) {
	if os.Getenv("FLEXWSPR_LIVE") == "" {
		t.Skip("set FLEXWSPR_LIVE=1 to query wspr.live")
	}
	c := New("FlexWSPR/test")
	rep, err := c.Get(context.Background(), "FN03", 43.5, -79, 500)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(rep.Bands, "", " ")
	t.Logf("%s\nday bands: %d, err %q", b, len(rep.Day), rep.Error)
	if len(rep.Bands) == 0 || len(rep.Day) == 0 {
		t.Fatal("no data")
	}
}
