package wspr

import (
	"os/exec"
	"syscall"
)

// hideWindow stops wsprd.exe opening a console window each cycle when
// FlexWSPR runs without a console (the desktop app).
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
