package area

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGetSharedAndErrors(t *testing.T) {
	var calls atomic.Int32
	fail := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if strings.Contains(r.URL.Query().Get("query"), "argMax") {
			w.Write([]byte(`{"data":[[14,"120",10,12,8000,40,20,"K1ABC FN42 > G0XYZ IO91"]]}`))
		} else {
			w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer srv.Close()

	// A failing first fetch is an error, and stays one until the retry.
	fail.Store(true)
	c := New("test")
	c.URL = srv.URL + "/"
	if _, err := c.Get(context.Background(), "FN03", 43.5, -79, 500); err == nil {
		t.Fatal("want an error")
	}
	if _, err := c.Get(context.Background(), "FN03", 43.5, -79, 500); err == nil {
		t.Fatal("cached failure: want an error")
	}

	// A caller that gives up does not cancel the fetch for the others.
	fail.Store(false)
	c = New("test")
	c.URL = srv.URL + "/"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.busy <- struct{}{} // someone else is fetching
	if _, err := c.Get(ctx, "FN03", 43.5, -79, 500); err == nil {
		t.Fatal("cancelled wait: want an error")
	}
	<-c.busy
	rep, err := c.Get(context.Background(), "FN03", 43.5, -79, 500)
	if err != nil || len(rep.Bands) != 1 || rep.Bands[0].Band != "20m" || rep.Bands[0].Paths != 120 || rep.Bands[0].K5 != 20 {
		t.Fatalf("got %+v, %v", rep, err)
	}
	n := calls.Load()
	if _, err := c.Get(context.Background(), "FN03", 43.5, -79, 500); err != nil || calls.Load() != n {
		t.Fatalf("second Get should come from the cache (%d calls, %v)", calls.Load()-n, err)
	}
}
