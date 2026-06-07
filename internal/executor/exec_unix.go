package executor

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/nagios"
	"github.com/northplane/northplane/internal/scheduler"
)

// runExec executes a Nagios plugin (SPEC §8.1, §13.1):
//   - argv array, never a shell
//   - empty environment except an explicit whitelist + optional macro env
//   - hard timeout: context cancel + SIGKILL to the process group
//   - stdout capped at MaxOutput; stderr captured separately
func (ex *Executor) runExec(ctx context.Context, job scheduler.Job, e *catalog.Entry) {
	started := time.Now()
	timeout := ex.timeoutFor(e)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	mc := ex.macroContext(e)
	argv, _ := mc.ExpandArgs(e.Argv)
	if len(argv) == 0 || argv[0] == "" {
		ex.emit(e.Object.ID, job.Planned, started, model.StateUnknown,
			nagios.Output{Text: "UNKNOWN - empty check command"}, false)
		return
	}
	path, err := ex.resolvePlugin(argv[0])
	if err != nil {
		ex.emit(e.Object.ID, job.Planned, started, model.StateUnknown,
			nagios.Output{Text: "UNKNOWN - plugin not allowed: " + argv[0]}, false)
		return
	}

	cmd := exec.CommandContext(cctx, path, argv[1:]...)
	// Environment: minimal whitelist; macro env only when the command
	// opts in (env injection costs at high rates, SPEC §8.2).
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LC_ALL=C"}
	if ex.opt.ArtifactsDir != "" {
		env = append(env, "NORTHPLANE_ARTIFACT_DIR="+ex.opt.ArtifactsDir)
	}
	if e.EnvOn {
		env = append(env, mc.EnvVars()...)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = newCappedWriter(&stdout, nagios.MaxOutput)
	cmd.Stderr = newCappedWriter(&stderr, 16*1024)

	// New process group so the timeout kill reaps grandchildren too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second

	runErr := cmd.Run()
	timedOut := cctx.Err() == context.DeadlineExceeded

	var state model.State
	var out nagios.Output
	switch {
	case timedOut:
		state = model.StateUnknown
		out = nagios.Output{Text: "UNKNOWN - plugin timed out after " + timeout.String() +
			" (killed)"} // timeout is diagnosed as such (SPEC §8.1)
		ex.bumpTimeouts()
	case runErr == nil:
		state = model.StateOK
		out = nagios.ParseOutput(stdout.String())
	default:
		if ee, ok := runErr.(*exec.ExitError); ok {
			state = nagios.ExitState(ee.ExitCode())
			if ee.ExitCode() > 3 || ee.ExitCode() < 0 {
				state = model.StateUnknown
			}
			out = nagios.ParseOutput(stdout.String())
			if out.Text == "" {
				out.Text = "UNKNOWN - plugin exited " + ee.String()
			}
		} else {
			state = model.StateUnknown
			out = nagios.Output{Text: "UNKNOWN - cannot execute plugin: " + runErr.Error()}
		}
	}
	// stderr is diagnostic detail, not status text (SPEC §8.1)
	if s := strings.TrimSpace(stderr.String()); s != "" {
		if out.LongText != "" {
			out.LongText += "\n"
		}
		out.LongText += "[stderr] " + s
	}
	ex.emit(e.Object.ID, job.Planned, started, state, out, timedOut)
}

// cappedWriter discards beyond a limit (output cap, SPEC §8.1).
type cappedWriter struct {
	dst *bytes.Buffer
	max int
}

func newCappedWriter(dst *bytes.Buffer, max int) *cappedWriter {
	return &cappedWriter{dst: dst, max: max}
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w.dst.Len() >= w.max {
		return len(p), nil // swallow
	}
	room := w.max - w.dst.Len()
	if len(p) > room {
		w.dst.Write(p[:room])
		return len(p), nil
	}
	return w.dst.Write(p)
}
