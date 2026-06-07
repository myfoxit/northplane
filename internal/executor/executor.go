// Package executor runs due checks: worker pools per check class
// (SPEC §7.4) — builtin in-process Go checks (no fork, 10k+ parallel)
// and exec Nagios plugins with a bounded pool, hard timeouts via
// context + process-group kill, an empty environment whitelist and argv
// arrays (never a shell) per the threat model (SPEC §13.1).
package executor

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/checks"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/nagios"
	"github.com/northplane/northplane/internal/scheduler"
)

// Options tune the pools.
type Options struct {
	ExecPoolSize    int // 0 → min(256, 32×vCPU) (SPEC §7.4)
	BuiltinPoolSize int // 0 → 1024
	PluginsDir      string
	PluginsAllow    []string // optional basename allowlist (SPEC §13.1)
	DefaultTimeout  time.Duration
	ArtifactsDir    string // NORTHPLANE_ARTIFACT_DIR for E2E checks (SPEC §8.6)
	// Secrets resolves $SECRET:name$ macros.
	Secrets func(tenantID, name string) (string, bool)
}

// Executor consumes scheduler jobs and emits results on the bus.
type Executor struct {
	opt Options
	cat *catalog.Catalog
	bus *eventbus.Bus
	log *slog.Logger

	sem     chan struct{} // exec pool
	allowed map[string]bool

	statRunning  int64
	statTimeouts uint64
	mu           sync.Mutex
}

// New builds an executor.
func New(opt Options, cat *catalog.Catalog, bus *eventbus.Bus, log *slog.Logger) *Executor {
	if opt.ExecPoolSize <= 0 {
		opt.ExecPoolSize = min(256, 32*runtime.NumCPU())
	}
	if opt.BuiltinPoolSize <= 0 {
		opt.BuiltinPoolSize = 1024
	}
	if opt.DefaultTimeout <= 0 {
		opt.DefaultTimeout = 30 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	ex := &Executor{opt: opt, cat: cat, bus: bus, log: log,
		sem: make(chan struct{}, opt.ExecPoolSize)}
	if len(opt.PluginsAllow) > 0 {
		ex.allowed = map[string]bool{}
		for _, a := range opt.PluginsAllow {
			ex.allowed[a] = true
		}
	}
	return ex
}

// Run consumes the scheduler queues until ctx ends. Priority jobs are
// drained first (SPEC §7.4 priority lane).
func (ex *Executor) Run(ctx context.Context, sched *scheduler.Scheduler) {
	builtinSem := make(chan struct{}, ex.opt.BuiltinPoolSize)
	var wg sync.WaitGroup
	for {
		var job scheduler.Job
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case job = <-sched.Priority:
		default:
			select {
			case <-ctx.Done():
				wg.Wait()
				return
			case job = <-sched.Priority:
			case job = <-sched.Out:
			}
		}
		entry := ex.cat.Get(job.ObjectID)
		if entry == nil {
			continue // deleted between scheduling and execution
		}
		switch entry.Class {
		case model.CommandBuiltin:
			wg.Add(1)
			builtinSem <- struct{}{}
			go func(j scheduler.Job, e *catalog.Entry) {
				defer func() { <-builtinSem; wg.Done() }()
				ex.runBuiltin(ctx, j, e)
			}(job, entry)
		case model.CommandExec:
			wg.Add(1)
			ex.sem <- struct{}{}
			go func(j scheduler.Job, e *catalog.Entry) {
				defer func() { <-ex.sem; wg.Done() }()
				ex.runExec(ctx, j, e)
			}(job, entry)
		case model.CommandPassive, model.CommandAgent:
			// freshness probe: synthesize staleness if no fresh result
			ex.checkFreshness(job, entry)
		}
	}
}

func (ex *Executor) emit(objectID string, planned time.Time, started time.Time,
	state model.State, out nagios.Output, timeout bool) {
	res := &model.CheckResult{
		ObjectID:   objectID,
		State:      state,
		Output:     out.Text,
		LongOutput: out.LongText,
		Perfdata:   out.Perfdata,
		At:         time.Now().UTC(),
		LatencyMS:  started.Sub(planned).Milliseconds(),
		ExecMS:     time.Since(started).Milliseconds(),
		Timeout:    timeout,
		Source:     "scheduler",
	}
	if res.LatencyMS < 0 {
		res.LatencyMS = 0
	}
	ex.bus.Results <- res
}

// macroContext assembles macro expansion inputs for an object.
func (ex *Executor) macroContext(e *catalog.Entry) *nagios.MacroContext {
	mc := &nagios.MacroContext{}
	if e.Object.Kind == model.KindHost {
		mc.Host = e.Object
		eff := e.Effective
		mc.HostSpec = &eff
	} else {
		mc.Service = e.Object
		eff := e.Effective
		mc.ServiceSpec = &eff
		if e.Host != nil {
			mc.Host = e.Host.Object
			heff := e.Host.Effective
			mc.HostSpec = &heff
		}
	}
	mc.Args = e.MacroArgs
	tenant := e.Object.TenantID
	if ex.opt.Secrets != nil {
		mc.Secrets = func(name string) (string, bool) { return ex.opt.Secrets(tenant, name) }
	}
	mc.User = func(n int) (string, bool) {
		if n == 1 {
			return ex.opt.PluginsDir, true
		}
		return "", false
	}
	return mc
}

func (ex *Executor) timeoutFor(e *catalog.Entry) time.Duration {
	if e.Effective.Timeout > 0 {
		return e.Effective.Timeout.D()
	}
	return ex.opt.DefaultTimeout
}

func (ex *Executor) runBuiltin(ctx context.Context, job scheduler.Job, e *catalog.Entry) {
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, ex.timeoutFor(e))
	defer cancel()

	mc := ex.macroContext(e)
	args, _ := mc.ExpandArgs(e.Argv)
	target := checks.Target{
		Address: e.Effective.Address,
		Vars:    e.Effective.Vars,
	}
	if e.Object.Kind == model.KindService && e.Host != nil {
		target.Address = e.Host.Effective.Address
		if target.Address == "" {
			target.Address = e.Host.Object.Name
		}
	}
	if target.Address == "" {
		target.Address = e.Object.Name
	}

	state, output := checks.Run(cctx, e.Builtin, target, args)
	timeout := cctx.Err() == context.DeadlineExceeded
	if timeout {
		state = model.StateUnknown
		output = nagios.Output{Text: "UNKNOWN - check timed out after " + ex.timeoutFor(e).String()}
		ex.bumpTimeouts()
	}
	ex.emit(e.Object.ID, job.Planned, started, state, output, timeout)
}

func (ex *Executor) checkFreshness(job scheduler.Job, e *catalog.Entry) {
	// The pipeline tracks last result times; the freshness probe just
	// emits a staleness result when nothing fresh arrived. Decision is
	// made by the pipeline (it owns check_state) — we forward a marker.
	ex.bus.Results <- &model.CheckResult{
		ObjectID: e.Object.ID,
		State:    model.StateUnknown,
		Output:   "", // pipeline replaces with staleness text iff stale
		At:       time.Now().UTC(),
		Source:   "freshness",
	}
	_ = job
}

func (ex *Executor) bumpTimeouts() {
	ex.mu.Lock()
	ex.statTimeouts++
	ex.mu.Unlock()
}

// Stats snapshot.
type Stats struct {
	ExecSlots    int    `json:"execSlotsBusy"`
	ExecCapacity int    `json:"execCapacity"`
	Timeouts     uint64 `json:"timeouts"`
}

// Stats for self-metrics.
func (ex *Executor) Stats() Stats {
	ex.mu.Lock()
	defer ex.mu.Unlock()
	return Stats{ExecSlots: len(ex.sem), ExecCapacity: cap(ex.sem), Timeouts: ex.statTimeouts}
}

// resolvePlugin locates argv[0] under PluginsDir unless absolute,
// enforcing the allowlist (SPEC §13.1).
func (ex *Executor) resolvePlugin(arg0 string) (string, error) {
	base := filepath.Base(arg0)
	if ex.allowed != nil && !ex.allowed[base] {
		return "", os.ErrPermission
	}
	if filepath.IsAbs(arg0) {
		return arg0, nil
	}
	if strings.Contains(arg0, "..") {
		return "", os.ErrPermission
	}
	return filepath.Join(ex.opt.PluginsDir, arg0), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
