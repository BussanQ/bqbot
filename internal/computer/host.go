package computer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"bqbot/internal/mcpgateway"
)

const DriverVersion = "0.28.2"
const HostBundleID = "dev.bussanq.bqbot"

// HostStatus contains no socket paths or process credentials.
type HostStatus struct {
	Available   bool             `json:"available"`
	Running     bool             `json:"running"`
	Version     string           `json:"version"`
	Error       string           `json:"error,omitempty"`
	Permissions PermissionStatus `json:"permissions"`
}

// Host owns a driver directly spawned by the desktop process. It never adopts a
// shared daemon, downloads executables, or replays actions after a driver exit.
type Host struct {
	mu          sync.Mutex
	run         *hostRun
	closed      bool
	lastError   string
	resolve     func() (string, bool, error)
	permissions func() (PermissionStatus, error)
	launch      func(context.Context, string, bool) (hostConnection, error)
}
type hostRun struct {
	ctx      context.Context
	cancel   context.CancelFunc
	ready    chan struct{}
	finished chan struct{}
	client   toolCaller
	err      error
}
type toolCaller interface {
	Call(context.Context, string, json.RawMessage) (json.RawMessage, error)
}
type hostConnection struct {
	client toolCaller
	done   <-chan struct{}
	close  func()
}

// NewHost is created only by the desktop owning the local service.
func NewHost() *Host {
	return &Host{resolve: resolveDriver, permissions: HostPermissions, launch: launchDriver}
}
func (h *Host) Status() HostStatus {
	state := HostStatus{Version: DriverVersion}
	if h == nil {
		state.Error = "本机控制由桌面应用提供；请退出独立 serve 后从桌面启动"
		return state
	}
	_, _, err := h.resolve()
	state.Available = err == nil
	if err != nil {
		state.Error = err.Error()
	}
	state.Permissions, err = h.permissions()
	if err != nil {
		state.Error = err.Error()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		state.Available = false
		state.Error = "desktop host is closed"
	}
	if h.lastError != "" {
		state.Error = h.lastError
	}
	if r := h.run; r != nil && r.ctx.Err() == nil {
		select {
		case <-r.ready:
			state.Running = r.err == nil
		default:
		}
	}
	return state
}
func (h *Host) Start(ctx context.Context) error {
	if h == nil {
		return errors.New("本机控制需要由桌面应用启动的服务")
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errors.New("desktop host is closed")
	}
	r := h.run
	if r == nil {
		runCtx, cancel := context.WithCancel(context.Background())
		r = &hostRun{ctx: runCtx, cancel: cancel, ready: make(chan struct{}), finished: make(chan struct{})}
		h.run = r
		h.lastError = ""
		go h.serve(r)
	}
	h.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.ready:
		if r.err != nil {
			return r.err
		}
		return r.ctx.Err()
	case <-r.ctx.Done():
		return errors.New("本机控制已停止，请重新启动")
	}
}
func (h *Host) serve(r *hostRun) {
	defer r.cancel()
	defer func() {
		h.mu.Lock()
		if h.run == r {
			h.run = nil
		}
		close(r.finished)
		h.mu.Unlock()
	}()
	start, cancel := context.WithTimeout(r.ctx, 20*time.Second)
	defer cancel()
	path, packaged, err := h.resolve()
	if err == nil {
		var p PermissionStatus
		p, err = h.permissions()
		if err == nil && (!p.Supported || !p.Accessibility || !p.ScreenRecording) {
			err = errors.New("请先授予 bqbot 辅助功能和屏幕录制权限，再启动本机控制")
		}
	}
	var conn hostConnection
	if err == nil {
		conn, err = h.launch(start, path, packaged)
	}
	if err != nil {
		h.mu.Lock()
		r.err = err
		h.lastError = err.Error()
		close(r.ready)
		h.mu.Unlock()
		return
	}
	defer conn.close()
	h.mu.Lock()
	r.client = conn.client
	close(r.ready)
	h.mu.Unlock()
	select {
	case <-r.ctx.Done():
	case <-conn.done:
		h.mu.Lock()
		h.lastError = "本机驱动已退出；检查权限后重新启动。未重试任何操作。"
		h.mu.Unlock()
		r.cancel()
	}
}

// Stop cancels startup and active calls, then waits for the daemon to be reaped.
func (h *Host) Stop() {
	if h == nil {
		return
	}
	h.mu.Lock()
	r := h.run
	if r != nil {
		r.cancel()
	}
	h.mu.Unlock()
	if r != nil {
		<-r.finished
	}
}
func (h *Host) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.Stop()
}

type hostSessionKey struct{}

// BindSession pins deferred approvals to the current driver instance. A restart
// invalidates those approvals even when the saved computer configuration matches.
func (h *Host) BindSession() func(context.Context) context.Context {
	var r *hostRun
	if h != nil {
		h.mu.Lock()
		r = h.run
		h.mu.Unlock()
	}
	return func(ctx context.Context) context.Context {
		return context.WithValue(ctx, hostSessionKey{}, r)
	}
}

func (h *Host) call(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if h == nil {
		return nil, errors.New("请从桌面应用启动本机控制")
	}
	h.mu.Lock()
	r := h.run
	if expected, bound := ctx.Value(hostSessionKey{}).(*hostRun); bound && expected != r {
		h.mu.Unlock()
		return nil, errors.New("本机控制已重启，请在新一轮对话中重新审批")
	}
	if r == nil || r.ctx.Err() != nil {
		h.mu.Unlock()
		return nil, errors.New("本机控制未启动，请在电脑面板中启动")
	}
	select {
	case <-r.ready:
	default:
		h.mu.Unlock()
		return nil, errors.New("本机控制仍在启动")
	}
	client, err := r.client, r.err
	h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	off := context.AfterFunc(r.ctx, cancel)
	defer off()
	return client.Call(callCtx, name, args)
}

func driverEnvironment() []string {
	// Do not inherit provider credentials, dynamic-loader injection, alternate Cua
	// sockets or permission bypass settings from the application's environment.
	env := []string{}
	for _, key := range []string{"HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "PATH"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env, "CUA_DRIVER_EMBEDDED=1", "CUA_DRIVER_HOST_BUNDLE_ID="+HostBundleID, "CUA_DRIVER_EMBEDDED_HOST_PID="+strconv.Itoa(os.Getpid()), "CUA_DRIVER_PERMISSION_MODE=standard", "CUA_DRIVER_RS_TELEMETRY_ENABLED=0")
}
func launchDriver(ctx context.Context, path string, packaged bool) (hostConnection, error) {
	// A short /tmp path avoids Unix socket length limits on macOS.
	dir, err := os.MkdirTemp("/tmp", "bqhost-")
	if err != nil {
		return hostConnection{}, err
	}
	socket := filepath.Join(dir, "driver.sock")
	cmd := exec.Command(path, "serve", "--embedded", "--parent-liveness-stdio", "--no-permissions-gate", "--socket", socket, "--host-bundle-id", HostBundleID, "--permission-mode", "standard")
	cmd.Env = driverEnvironment()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(dir)
		return hostConnection{}, err
	}
	if err = cmd.Start(); err != nil {
		stdin.Close()
		os.RemoveAll(dir)
		return hostConnection{}, errors.New("无法启动内置 Cua Driver")
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	var client *mcpgateway.Client
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if client != nil {
				client.Close()
			}
			stdin.Close()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
			os.RemoveAll(dir)
		})
	}
	ok := false
	defer func() {
		if !ok {
			cleanup()
		}
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, e := os.Lstat(socket)
		if e == nil {
			if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
				return hostConnection{}, errors.New("Cua socket must be private (0600)")
			}
			conn, e := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "unix", socket)
			if e == nil {
				conn.Close()
				break
			}
		}
		select {
		case <-ctx.Done():
			return hostConnection{}, errors.New("Cua Driver 启动超时或已取消")
		case <-done:
			return hostConnection{}, errors.New("Cua Driver 启动期间退出，请检查系统权限和版本")
		case <-ticker.C:
		}
	}
	// Startup cancellation also stops MCP initialization. Once initialized,
	// the session lives until cleanup instead of inheriting the startup deadline.
	proxyCtx, cancelProxy := context.WithCancel(context.Background())
	stopStartup := context.AfterFunc(ctx, cancelProxy)
	client, err = mcpgateway.Open(proxyCtx, mcpgateway.Config{Transport: "stdio", Command: path, Args: []string{"mcp", "--embedded", "--socket", socket}, Env: driverEnvironment()})
	stopStartup()
	if err != nil {
		cancelProxy()
		return hostConnection{}, err
	}
	if err = verifyDriver(ctx, client, packaged); err != nil {
		cancelProxy()
		return hostConnection{}, err
	}
	ok = true
	ended := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-client.Done():
		}
		close(ended)
	}()
	return hostConnection{client: client, done: ended, close: func() { cancelProxy(); cleanup() }}, nil
}
func verifyDriver(ctx context.Context, client toolCaller, packaged bool) error {
	raw, err := client.Call(ctx, "check_permissions", json.RawMessage(`{}`))
	if err != nil {
		return err
	}
	var result struct {
		IsError bool `json:"isError"`
		Data    struct {
			Accessibility bool `json:"accessibility"`
			Screen        bool `json:"screen_recording"`
			Source        struct {
				Attribution string `json:"attribution"`
				Bundle      string `json:"host_bundle_id"`
				Embedded    bool   `json:"embedded"`
			} `json:"source"`
		} `json:"structuredContent"`
	}
	if json.Unmarshal(raw, &result) != nil || result.IsError || !result.Data.Accessibility || !result.Data.Screen || result.Data.Source.Attribution != "host" || result.Data.Source.Bundle != HostBundleID || !result.Data.Source.Embedded {
		return errors.New("Cua 未确认宿主权限与内置模式，请授权后重启")
	}
	if !packaged {
		return nil
	}
	raw, err = client.Call(ctx, "health_report", json.RawMessage(`{"include":["bundle_identity"]}`))
	if err != nil {
		return err
	}
	var health struct {
		IsError bool `json:"isError"`
		Data    struct {
			Checks []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
				Data   struct {
					Bundle string `json:"bundle_identifier"`
					Source string `json:"identity_source"`
					PID    int    `json:"parent_process_id"`
				} `json:"data"`
			} `json:"checks"`
		} `json:"structuredContent"`
	}
	if json.Unmarshal(raw, &health) != nil || health.IsError {
		return errors.New("无法验证 Cua 宿主身份")
	}
	for _, c := range health.Data.Checks {
		if c.Name == "bundle_identity" && c.Status == "pass" && c.Data.Bundle == HostBundleID && c.Data.Source == "parent_application" && c.Data.PID == os.Getpid() {
			return nil
		}
	}
	return errors.New("Cua 权限身份不属于当前 bqbot 应用，拒绝本机控制")
}
func resolveDriver() (string, bool, error) {
	if runtime.GOOS != "darwin" {
		return "", false, errors.New("当前内置本机控制支持 macOS；此平台仍可连接 VM")
	}
	exe, err := os.Executable()
	if err != nil {
		return "", false, err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", false, err
	}
	packaged := filepath.Base(filepath.Dir(exe)) == "MacOS" && filepath.Base(filepath.Dir(filepath.Dir(exe))) == "Contents"
	var candidates []string
	if packaged {
		candidates = []string{filepath.Join(filepath.Dir(exe), "..", "Resources", "cua-driver", "cua-driver")}
	} else {
		cwd, _ := os.Getwd()
		candidates = []string{filepath.Join(filepath.Dir(exe), "..", "resources", "darwin", "cua-driver", "cua-driver"), filepath.Join(cwd, "resources", "darwin", "cua-driver", "cua-driver")}
	}
	for _, path := range candidates {
		path = filepath.Clean(path)
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return "", packaged, errors.New("内置驱动不是可执行的普通文件")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if packaged {
			err = exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", filepath.Dir(filepath.Dir(filepath.Dir(exe)))).Run()
		}
		cancel()
		if err != nil {
			return "", packaged, errors.New("bqbot 应用签名校验失败，请重新安装")
		}
		if !packaged {
			f, e := os.Open(path)
			if e != nil {
				return "", false, e
			}
			sum := sha256.New()
			_, e = io.Copy(sum, f)
			f.Close()
			if e != nil || fmt.Sprintf("%x", sum.Sum(nil)) != driverSHA256 {
				return "", false, errors.New("内置驱动校验失败，请重新运行 python3 -I scripts/prepare-cua.py")
			}
		}
		return path, packaged, nil
	}
	return "", packaged, fmt.Errorf("缺少内置 Cua Driver %s，请使用完整安装包或运行 python3 -I scripts/prepare-cua.py", DriverVersion)
}

const driverSHA256 = "af30d29cf33bd3bbda1330be7225b18881ea4c5af6df374e08627914b5ac334d"
