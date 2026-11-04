// flexwspr-gui is the FlexWSPR desktop app for Windows and Linux: the same
// receiver as the headless program, with its GUI in a window (Wails).
//
// Build (no Wails CLI needed):
//
//	Windows: go build -tags desktop,production -ldflags "-H windowsgui" ./cmd/flexwspr-gui
//	Linux:   go build -tags desktop,production,webkit2_41 ./cmd/flexwspr-gui
//	         (needs CGO and libgtk-3-dev, libwebkit2gtk-4.1-dev)
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/va3mw/FlexWSPR/internal/app"
	"github.com/va3mw/FlexWSPR/internal/config"
	"github.com/va3mw/FlexWSPR/internal/web"
)

var version = "0.4.2" // set by the build scripts

func main() {
	cfgPath := flag.String("config", "", "configuration file")
	flag.Parse()

	path := config.Find(*cfgPath)
	cfg, saved, err := config.LoadOrDefault(path)
	if err != nil {
		// Start anyway on defaults so the settings can be fixed in the window.
		cfg = config.Default()
		cfg.Resolve()
		saved = false
	}
	os.MkdirAll(cfg.DataDir, 0o755)
	logOut := openLog(filepath.Join(cfg.DataDir, "flexwspr.log"))
	st := app.NewStation(path, cfg, saved, version, logOut)
	if err != nil {
		st.Log().Error("config file could not be read; using defaults", "file", path, "err", err)
	}

	// Serve the same GUI as a web page too (web.listen; Windows default
	// 127.0.0.1:8080, ":8080" for other computers, "off" for none), so the
	// station can be watched from a phone or another PC. Like the headless
	// program's page, it cannot change program paths.
	defer web.Start(cfg.Web.Listen, st, true).Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { st.Run(ctx); close(done) }()

	err = wails.Run(&options.App{
		Title:     "FlexWSPR " + version,
		Width:     1200,
		Height:    820,
		MinWidth:  720,
		MinHeight: 480,
		AssetServer: &assetserver.Options{
			Handler: web.Handler(st, web.Options{Local: true}),
		},
		SingleInstanceLock: &options.SingleInstanceLock{UniqueId: "6f2c5d1e-flexwspr-gui"},
		OnShutdown: func(context.Context) {
			cancel()
			<-done // remove our panadapter, slice and stream from the radio
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		cancel()
		<-done
		os.Exit(1)
	}
}

// openLog appends to the log file, keeping one previous file once it passes
// 10 MB. With no file it logs to stderr.
func openLog(path string) io.Writer {
	if fi, err := os.Stat(path); err == nil && fi.Size() > 10<<20 {
		os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return os.Stderr
	}
	return f
}
