//go:build !windows

package wspr

import "os/exec"

func hideWindow(*exec.Cmd) {}
