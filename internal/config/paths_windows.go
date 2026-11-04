//go:build windows

package config

import (
	"os"
	"os/exec"
	"path/filepath"
)

// DefaultConfigPath is flexwspr.yaml in %ProgramData%\FlexWSPR.
func DefaultConfigPath() string { return filepath.Join(programData(), "FlexWSPR", "flexwspr.yaml") }

// DefaultDataDir is %ProgramData%\FlexWSPR\data.
func DefaultDataDir() string { return filepath.Join(programData(), "FlexWSPR", "data") }

// DefaultWsprd finds wsprd.exe from a WSJT-X install, or on PATH.
func DefaultWsprd() string {
	if p, err := exec.LookPath("wsprd.exe"); err == nil {
		return p
	}
	for _, dir := range []string{`C:\WSJT\wsjtx\bin`, filepath.Join(os.Getenv("ProgramFiles"), "wsjtx", "bin")} {
		p := filepath.Join(dir, "wsprd.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "wsprd.exe"
}

// DefaultListen keeps the web page on this PC (no firewall prompt); set
// web.listen to ":8080" to reach it from other computers.
const DefaultListen = "127.0.0.1:8080"

func programData() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return d
	}
	return `C:\ProgramData`
}
