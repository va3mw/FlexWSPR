// FlexWSPR is a WSPR receiver for FlexRadio: it takes a DAX IQ stream from
// the radio, records each two-minute cycle, decodes it with wsprd, can follow
// a band schedule and upload the spots to wsprnet.org.
//
// This is the headless program (Debian, Raspberry Pi, Windows). Its GUI is a
// web page it serves itself; cmd/flexwspr-gui is the desktop app.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/va3mw/FlexWSPR/internal/app"
	"github.com/va3mw/FlexWSPR/internal/config"
	"github.com/va3mw/FlexWSPR/internal/web"
	"github.com/va3mw/FlexWSPR/internal/wspr"
)

var version = "0.5.0" // set by the build scripts

func main() {
	cfgPath := flag.String("config", "", "configuration file (default: "+config.DefaultConfigPath()+
		", or flexwspr.yaml next to the program)")
	listen := flag.String("listen", "", "web page address, e.g. :8080 or off (overrides web.listen)")
	decode := flag.String("decode", "", "decode an existing .c2 file with wsprd and exit")
	showVer := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVer {
		fmt.Println("FlexWSPR", version)
		return
	}

	path := config.Find(*cfgPath)
	cfg, saved, err := config.LoadOrDefault(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *decode != "" {
		os.Exit(decodeFile(ctx, cfg, *decode))
	}

	st := app.NewStation(path, cfg, saved, version, os.Stderr)
	log := st.Log()
	if !saved {
		log.Info("no configuration file yet; open the web page to set up", "will_save_to", path)
	}
	addr := cfg.Web.Listen
	if *listen != "" {
		addr = *listen
	}
	// A -listen flag fixes the address; otherwise it follows the settings.
	defer web.Start(addr, st, *listen == "").Close()
	st.Run(ctx)
}

// decodeFile runs wsprd on a saved .c2 file, for checking wsprd and the
// flip_c2 setting offline.
func decodeFile(ctx context.Context, cfg *config.Config, path string) int {
	abs, _ := filepath.Abs(path)
	start, err := time.Parse("060102_1504", strings.TrimSuffix(filepath.Base(path), ".c2"))
	if err != nil {
		start = time.Now().UTC()
	}
	d := &wspr.Decoder{Path: cfg.Wsprd.Path, Args: cfg.Wsprd.Args, DataDir: cfg.DataDir}
	spots, err := d.Decode(ctx, abs, start)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, s := range spots {
		fmt.Println(s.Format())
	}
	fmt.Fprintf(os.Stderr, "%d spot(s)\n", len(spots))
	return 0
}
