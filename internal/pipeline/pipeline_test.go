package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/executor"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/scheduler"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/tsdb"
)

// End-to-end M0 core loop: schedule → execute (builtin tcp + exec
// plugin) → state machine → storage + TSDB + events.
func TestCoreLoopEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()

	store, err := storage.Open(ctx, storage.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ts, err := tsdb.Open(filepath.Join(dir, "tsdb"), nil, tsdb.Retention{})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	// local TCP listener = healthy target
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	// fake Nagios plugin
	plugDir := t.TempDir()
	plugin := filepath.Join(plugDir, "check_dummy")
	script := "#!/bin/sh\necho \"DUMMY OK - everything fine | value=42MB;50;60;0;100\"\nexit 0\n"
	if err := os.WriteFile(plugin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	host := &model.Object{TenantID: model.DefaultTenant, Kind: model.KindHost, Name: "local-host",
		Labels: model.Labels{"env": "test"},
		Spec: model.ObjectSpec{Address: "127.0.0.1", CheckCommand: "builtin:tcp",
			Args:     []string{"-p", fmt.Sprint(port)},
			Interval: model.Duration(1 * time.Second), MaxCheckAttempts: 1}}
	if err := store.CreateObject(ctx, host); err != nil {
		t.Fatal(err)
	}
	svc := &model.Object{TenantID: model.DefaultTenant, Kind: model.KindService,
		Name: "dummy-plugin", HostID: host.ID,
		Spec: model.ObjectSpec{CheckCommand: "exec:check_dummy",
			Interval: model.Duration(1 * time.Second), MaxCheckAttempts: 1}}
	if err := store.CreateObject(ctx, svc); err != nil {
		t.Fatal(err)
	}

	cat := catalog.New(store)
	if err := cat.LoadAll(ctx); err != nil {
		t.Fatal(err)
	}
	bus := eventbus.New()
	sched := scheduler.New(cat, nil)
	for _, e := range cat.All() {
		sched.Upsert(e)
	}
	ex := executor.New(executor.Options{PluginsDir: plugDir}, cat, bus, nil)
	pl := New(store, cat, bus, ts, sched, nil)

	go sched.Run(ctx)
	go ex.Run(ctx, sched)
	go pl.Run(ctx)

	// drain alerting queue so the bus doesn't fill
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-bus.Events:
			}
		}
	}()

	// force immediate first runs (splay may park them up to interval)
	sched.CheckNow(host.ID)
	sched.CheckNow(svc.ID)

	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-deadline:
			hs, _ := store.GetCheckState(ctx, host.ID)
			ss, _ := store.GetCheckState(ctx, svc.ID)
			t.Fatalf("timeout: host=%+v svc=%+v", hs, ss)
		case <-time.After(300 * time.Millisecond):
		}
		hs, err1 := store.GetCheckState(ctx, host.ID)
		ss, err2 := store.GetCheckState(ctx, svc.ID)
		if err1 != nil || err2 != nil {
			continue
		}
		if hs.LastCheck == nil || ss.LastCheck == nil {
			continue
		}
		if hs.State == model.HostUp && ss.State == model.StateOK {
			if ss.Output == "" || ss.Perfdata == "" {
				t.Fatalf("plugin output lost: %+v", ss)
			}
			// TSDB got the perfdata series
			res, err := ts.Query(ctx, tsdb.Query{ObjectID: svc.ID, Metric: "value",
				From: time.Now().Add(-time.Minute), To: time.Now().Add(time.Minute),
				Step: time.Second, Agg: tsdb.AggLast})
			if err != nil {
				t.Fatal(err)
			}
			if len(res) != 1 || len(res[0].Points) == 0 {
				t.Fatalf("perfdata not in tsdb: %+v", res)
			}
			if res[0].Series.Unit != "bytes" || res[0].Points[0].V != 42*1024*1024 {
				t.Fatalf("normalization: %+v", res[0])
			}
			return // success
		}
	}
}

// Exec timeout: plugin must be killed (process group) and report UNKNOWN.
func TestExecTimeoutKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	store, err := storage.Open(ctx, storage.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	plugDir := t.TempDir()
	plugin := filepath.Join(plugDir, "check_hang")
	script := "#!/bin/sh\nsleep 60 &\nwait\n"
	if err := os.WriteFile(plugin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	obj := &model.Object{TenantID: model.DefaultTenant, Kind: model.KindHost, Name: "hang-host",
		Spec: model.ObjectSpec{CheckCommand: "exec:check_hang",
			Timeout: model.Duration(1 * time.Second), MaxCheckAttempts: 1,
			Interval: model.Duration(1 * time.Hour)}}
	if err := store.CreateObject(ctx, obj); err != nil {
		t.Fatal(err)
	}
	cat := catalog.New(store)
	if err := cat.LoadAll(ctx); err != nil {
		t.Fatal(err)
	}
	bus := eventbus.New()
	sched := scheduler.New(cat, nil)
	ex := executor.New(executor.Options{PluginsDir: plugDir}, cat, bus, nil)
	go ex.Run(ctx, sched)

	sched.CheckNow(obj.ID)
	select {
	case res := <-bus.Results:
		if !res.Timeout || res.State != model.StateUnknown {
			t.Fatalf("want timeout UNKNOWN, got %+v", res)
		}
		if res.ExecMS > 5000 {
			t.Fatalf("kill took too long: %dms", res.ExecMS)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no result — process group kill failed")
	}
}

// Soft→hard with retry interval: 3 attempts at retry cadence.
func TestSoftHardRetryCadence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	store, err := storage.Open(ctx, storage.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// closed port → CRITICAL
	obj := &model.Object{TenantID: model.DefaultTenant, Kind: model.KindService, Name: "down-svc",
		Spec: model.ObjectSpec{Address: "127.0.0.1", CheckCommand: "builtin:tcp",
			Args:          []string{"-p", "1"}, // port 1: closed
			Interval:      model.Duration(time.Hour),
			RetryInterval: model.Duration(300 * time.Millisecond),
			MaxCheckAttempts: 3, Timeout: model.Duration(2 * time.Second)}}
	host := &model.Object{TenantID: model.DefaultTenant, Kind: model.KindHost, Name: "down-host",
		Spec: model.ObjectSpec{Address: "127.0.0.1", CheckCommand: "passive"}}
	if err := store.CreateObject(ctx, host); err != nil {
		t.Fatal(err)
	}
	obj.HostID = host.ID
	if err := store.CreateObject(ctx, obj); err != nil {
		t.Fatal(err)
	}

	cat := catalog.New(store)
	if err := cat.LoadAll(ctx); err != nil {
		t.Fatal(err)
	}
	bus := eventbus.New()
	sched := scheduler.New(cat, nil)
	ex := executor.New(executor.Options{}, cat, bus, nil)
	ts, _ := tsdb.Open(filepath.Join(dir, "tsdb"), nil, tsdb.Retention{})
	defer ts.Close()
	pl := New(store, cat, bus, ts, sched, nil)
	go sched.Run(ctx)
	go ex.Run(ctx, sched)
	go pl.Run(ctx)

	var hardEvents int
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-bus.Events:
				if e.Type == model.EventStateChange {
					var p model.StateChangePayload
					_ = jsonUnmarshal(e.Payload, &p)
					if p.StateType == model.StateHard && p.To == model.StateCritical {
						hardEvents++
					}
				}
			}
		}
	}()

	sched.CheckNow(obj.ID)
	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-deadline:
			cs, _ := store.GetCheckState(ctx, obj.ID)
			t.Fatalf("never went hard: %+v", cs)
		case <-time.After(250 * time.Millisecond):
		}
		cs, err := store.GetCheckState(ctx, obj.ID)
		if err != nil {
			continue
		}
		if cs.State == model.StateCritical && cs.StateType == model.StateHard {
			if cs.Attempt != 3 {
				t.Fatalf("attempts: %d", cs.Attempt)
			}
			return
		}
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
