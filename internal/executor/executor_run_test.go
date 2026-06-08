package executor

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/scheduler"
)

// builtinEntry builds a builtin-class entry. An unknown builtin name lets
// runBuiltin exercise its full path (macro context, arg expansion, target
// resolution, checks.Run dispatch, emit) deterministically and offline:
// checks.Run returns UNKNOWN for an unregistered name.
func builtinEntry(name string) *catalog.Entry {
	obj := &model.Object{ID: "builtin-obj", TenantID: model.DefaultTenant,
		Kind: model.KindHost, Name: "builtin-obj"}
	return &catalog.Entry{
		Object:    obj,
		Effective: model.ObjectSpec{Address: "127.0.0.1", Timeout: model.Duration(time.Second)},
		Class:     model.CommandBuiltin,
		Builtin:   name,
	}
}

func TestRunBuiltinUnknownCheck(t *testing.T) {
	ex := newExec(Options{DefaultTimeout: time.Second})
	job := scheduler.Job{ObjectID: "builtin-obj", Planned: time.Now()}
	e := builtinEntry("__no_such_builtin__")
	res := drainResult(t, ex, func() { ex.runBuiltin(context.Background(), job, e) })
	if res.State != model.StateUnknown {
		t.Fatalf("state = %v, want UNKNOWN for unknown builtin", res.State)
	}
	if res.Timeout {
		t.Fatal("Timeout flag set for a non-timeout result")
	}
	if res.ObjectID != "builtin-obj" {
		t.Fatalf("objectID = %q", res.ObjectID)
	}
}

func TestRunBuiltinTargetAddressFallback(t *testing.T) {
	ex := newExec(Options{DefaultTimeout: time.Second})
	job := scheduler.Job{ObjectID: "svc", Planned: time.Now()}

	// Service with no address inherits its host's address; if that is empty
	// the host name is used. Exercise the service+host branch of runBuiltin.
	hostObj := &model.Object{ID: "h", TenantID: model.DefaultTenant, Kind: model.KindHost, Name: "host-name"}
	hostEnt := &catalog.Entry{Object: hostObj, Effective: model.ObjectSpec{}}
	svcObj := &model.Object{ID: "svc", TenantID: model.DefaultTenant, Kind: model.KindService, Name: "svc", HostID: "h"}
	e := &catalog.Entry{
		Object:    svcObj,
		Effective: model.ObjectSpec{Timeout: model.Duration(time.Second)},
		Class:     model.CommandBuiltin,
		Builtin:   "__no_such_builtin__",
		Host:      hostEnt,
	}
	res := drainResult(t, ex, func() { ex.runBuiltin(context.Background(), job, e) })
	if res.State != model.StateUnknown {
		t.Fatalf("state = %v, want UNKNOWN", res.State)
	}
}

func TestCheckFreshnessEmitsMarker(t *testing.T) {
	ex := newExec(Options{})
	obj := &model.Object{ID: "passive", TenantID: model.DefaultTenant, Kind: model.KindService, Name: "passive"}
	e := &catalog.Entry{Object: obj, Effective: model.ObjectSpec{}, Class: model.CommandPassive}
	job := scheduler.Job{ObjectID: "passive", Planned: time.Now()}

	res := drainResult(t, ex, func() { ex.checkFreshness(job, e) })
	if res.Source != "freshness" {
		t.Fatalf("source = %q, want freshness", res.Source)
	}
	if res.State != model.StateUnknown {
		t.Fatalf("state = %v, want UNKNOWN marker", res.State)
	}
	if res.Output != "" {
		t.Fatalf("output = %q, want empty (pipeline fills staleness text)", res.Output)
	}
}

// TestRunDispatch drives the full Run loop: a real catalog (no store access
// for an exec object) + scheduler channel feed one exec job through the
// executor and assert it emits a result, then ctx cancellation stops Run.
func TestRunDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell semantics required")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	cat := catalog.New(nil) // exec objects index without touching the store
	obj := &model.Object{
		ID: "rundispatch", TenantID: model.DefaultTenant, Kind: model.KindHost, Name: "rundispatch",
		Spec: model.ObjectSpec{CheckCommand: "exec:/bin/sh", Args: []string{"-c", "echo 'OK - run'; exit 0"}},
	}
	if err := cat.UpsertObject(obj); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	ex := New(Options{DefaultTimeout: 5 * time.Second}, cat, eventbus.New(), nil)
	sched := scheduler.New(cat, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { ex.Run(ctx, sched); close(done) }()

	sched.Out <- scheduler.Job{ObjectID: "rundispatch", Planned: time.Now()}

	select {
	case res := <-ex.bus.Results:
		if res.State != model.StateOK || res.Output != "OK - run" {
			t.Fatalf("result = %+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not dispatch the job")
	}

	// Cancellation drains in-flight work and returns.
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}
