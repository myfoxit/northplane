//go:build windows

package executor

import (
	"os/exec"
	"strconv"
	"syscall"
)

// defaultPluginPath is the PATH handed to exec plugins on Windows.
const defaultPluginPath = `C:\Windows\System32;C:\Windows`

// setProcAttr starts the plugin in a new process group (so a console signal
// sent to northplaned does not propagate to plugins) and, on timeout/cancel,
// terminates the whole process tree. Windows has no POSIX process groups or
// SIGKILL, so we use taskkill /T to reap descendants, falling back to a
// single-process kill if taskkill is unavailable.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		kill := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid))
		if err := kill.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
