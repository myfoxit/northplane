//go:build unix

package executor

import (
	"os/exec"
	"syscall"
)

// defaultPluginPath is the PATH handed to exec plugins on Unix.
const defaultPluginPath = "/usr/local/bin:/usr/bin:/bin"

// setProcAttr puts the plugin in its own process group so the timeout kill
// reaps grandchildren too, and wires Cancel to SIGKILL that whole group
// (negative PID == the process group).
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
}
