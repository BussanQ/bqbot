package computer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bqbot/internal/store"
)

type fakeHostTool func(context.Context, string, json.RawMessage) (json.RawMessage, error)

func (f fakeHostTool) Call(ctx context.Context, n string, a json.RawMessage) (json.RawMessage, error) {
	return f(ctx, n, a)
}
func testHost(launch func(context.Context, string, bool) (hostConnection, error)) *Host {
	return &Host{resolve: func() (string, bool, error) { return "fake", false, nil }, permissions: func() (PermissionStatus, error) {
		return PermissionStatus{Supported: true, Accessibility: true, ScreenRecording: true}, nil
	}, launch: launch}
}
func TestHostLifecycleAndApprovalGeneration(t *testing.T) {
	var starts, closes, calls atomic.Int32
	entered := make(chan struct{}, 1)
	h := testHost(func(context.Context, string, bool) (hostConnection, error) {
		starts.Add(1)
		return hostConnection{client: fakeHostTool(func(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
			calls.Add(1)
			entered <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}), done: make(chan struct{}), close: func() { closes.Add(1) }}, nil
	})
	defer h.Close()
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if err := h.Start(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if starts.Load() != 1 || !h.Status().Running {
		t.Fatal("concurrent starts did not coalesce")
	}
	bind := h.BindSession()
	result := make(chan error, 1)
	go func() { _, e := h.call(bind(context.Background()), "click", nil); result <- e }()
	<-entered
	h.Stop()
	if e := <-result; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if closes.Load() != 1 || h.Status().Running {
		t.Fatal("stop did not clean up")
	}
	if e := h.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := h.call(bind(context.Background()), "click", nil); e == nil {
		t.Fatal("old approval survived restart")
	}
	if calls.Load() != 1 {
		t.Fatal("action was replayed")
	}
	h.Close()
	if h.Start(context.Background()) == nil {
		t.Fatal("closed host restarted")
	}
}
func TestHostStopDuringStartupAndUnexpectedExit(t *testing.T) {
	entered := make(chan struct{})
	h := testHost(func(ctx context.Context, _ string, _ bool) (hostConnection, error) {
		close(entered)
		<-ctx.Done()
		return hostConnection{}, ctx.Err()
	})
	started := make(chan error, 1)
	go func() { started <- h.Start(context.Background()) }()
	<-entered
	h.Stop()
	if <-started == nil {
		t.Fatal("canceled startup succeeded")
	}
	if h.Status().Running {
		t.Fatal("canceled host running")
	}
	exited := make(chan struct{})
	cleaned := make(chan struct{})
	h.launch = func(context.Context, string, bool) (hostConnection, error) {
		return hostConnection{client: fakeHostTool(func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return nil, nil }), done: exited, close: func() { close(cleaned) }}, nil
	}
	if e := h.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	close(exited)
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("exit not cleaned up")
	}
	if h.Status().Running {
		t.Fatal("dead driver reported running")
	}
	if _, e := h.call(context.Background(), "click", nil); e == nil {
		t.Fatal("dead driver accepted action")
	}
	h.Close()
}
func TestHostConsentPermissionsAndHeadless(t *testing.T) {
	h := testHost(func(context.Context, string, bool) (hostConnection, error) {
		t.Fatal("unauthorized launch")
		return hostConnection{}, nil
	})
	h.permissions = func() (PermissionStatus, error) { return PermissionStatus{Supported: true}, nil }
	if h.Start(context.Background()) == nil {
		t.Fatal("missing permissions accepted")
	}
	h.Close()
	c := store.ComputerConfig{BotID: "bot", Provider: "host", Enabled: true}
	if _, e := ExecuteWithHost(context.Background(), c, Action{Kind: "click"}, h); e == nil {
		t.Fatal("consent missing")
	}
	c.HostConsent = true
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	if _, e := Execute(context.Background(), c, Action{Kind: "click"}); e == nil {
		t.Fatal("headless acquired driver")
	}
	c.TokenEnv = "SECRET"
	if c.Validate() == nil {
		t.Fatal("built-in accepted token override")
	}
}
func TestHostDriverEnvironment(t *testing.T) {
	for _, k := range []string{"DYLD_INSERT_LIBRARIES", "CUA_DRIVER_PERMISSION_MODE", "CUA_DRIVER_SOCKET_PATH", "OPENAI_API_KEY"} {
		t.Setenv(k, "unsafe")
	}
	for _, v := range driverEnvironment() {
		if strings.Contains(v, "unsafe") {
			t.Fatal("inherited unsafe environment", strings.SplitN(v, "=", 2)[0])
		}
	}
}
func TestHostDriverIdentity(t *testing.T) {
	valid := json.RawMessage(`{"structuredContent":{"accessibility":true,"screen_recording":true,"source":{"attribution":"host","host_bundle_id":"dev.bussanq.bqbot","embedded":true}}}`)
	tool := fakeHostTool(func(_ context.Context, n string, _ json.RawMessage) (json.RawMessage, error) {
		if n == "check_permissions" {
			return valid, nil
		}
		return json.RawMessage(`{"structuredContent":{"checks":[{"name":"bundle_identity","status":"pass","data":{"bundle_identifier":"dev.bussanq.bqbot","identity_source":"parent_application","parent_process_id":1}}]}}`), nil
	})
	if e := verifyDriver(context.Background(), tool, false); e != nil {
		t.Fatal(e)
	}
	if verifyDriver(context.Background(), tool, true) == nil {
		t.Fatal("wrong parent accepted")
	}
	valid = json.RawMessage(`{"structuredContent":{"accessibility":true,"screen_recording":true,"source":{"attribution":"driver-daemon"}}}`)
	if verifyDriver(context.Background(), tool, false) == nil {
		t.Fatal("standalone driver accepted")
	}
}
func TestHostActionProjection(t *testing.T) {
	for _, a := range []Action{{Kind: "key", Key: "Page_Up"}, {Kind: "scroll", X: 15, Y: 42, Direction: "down"}} {
		_, e := cuaAction(context.Background(), func(_ context.Context, n string, raw json.RawMessage) (json.RawMessage, error) {
			var args map[string]any
			if e := json.Unmarshal(raw, &args); e != nil {
				t.Fatal(e)
			}
			if args["scope"] != "desktop" {
				t.Fatal(args)
			}
			if n == "press_key" && args["key"] != "PageUp" {
				t.Fatal(args)
			}
			if n == "scroll" && (args["x"] != float64(15) || args["y"] != float64(42)) {
				t.Fatal(args)
			}
			return json.RawMessage(`{"content":[]}`), nil
		}, a)
		if e != nil {
			t.Fatal(e)
		}
	}
}
