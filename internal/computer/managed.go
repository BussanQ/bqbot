package computer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bqbot/internal/store"
	"github.com/egoist/mygo"
)

type localDesktop struct {
	dir, name string
}

func localFor(st *store.Store, id string) (localDesktop, error) {
	dir, err := st.ManagedWorkspaceDir(id)
	sum := sha256.Sum256([]byte(dir))
	return localDesktop{dir, fmt.Sprintf("bqbot-ws-%x", sum[:12])}, err
}

func docker(ctx context.Context, token string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if token != "" {
		cmd.Env = append(os.Environ(), "BQBOT_WORKSPACE_TOKEN="+token)
	}
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("未找到 Docker，请安装并启动 Docker Desktop 或 Docker Engine")
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			msg := strings.TrimSpace(string(exit.Stderr))
			if token != "" {
				msg = strings.ReplaceAll(msg, token, "***")
			}
			if len(msg) > 1200 {
				msg = msg[len(msg)-1200:]
			}
			return "", fmt.Errorf("Docker %s：%s", args[0], msg)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (d localDesktop) container(ctx context.Context) (string, error) {
	// Filter by both name and owner so we never operate on unrelated containers.
	return docker(ctx, "", "ps", "-aq", "--no-trunc", "--filter", "name=^/"+d.name+"$", "--filter", "label=dev.bqbot.workspace="+d.name)
}

func (d localDesktop) credential() (string, error) {
	path := filepath.Join(d.dir, "token")
	data, err := os.ReadFile(path)
	if err == nil {
		if raw, e := hex.DecodeString(string(data)); e != nil || len(raw) != 32 {
			return "", errors.New("工作区凭据文件无效")
		}
		return string(data), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var secret [32]byte
	if _, err = rand.Read(secret[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(secret[:])
	return token, atomicWorkspaceFile(path, token)
}

func atomicWorkspaceFile(path, value string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".workspace-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(value); err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

const desktopRevision = "xfce-v1"
const managedImage = "bqbot-workspace:" + desktopRevision

func workspaceImage(ctx context.Context) error {
	if _, err := docker(ctx, "", "image", "inspect", "--format", "{{.Id}}", managedImage); err == nil {
		return nil
	}
	res, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return err
	}
	dir := filepath.Join(res, "workspace-image")
	file := filepath.Join(dir, "workspace", "Dockerfile")
	if _, err = os.Stat(file); err != nil {
		return errors.New("缺少 Linux 工作区镜像构建资源。开发版请在仓库运行 python3 -I scripts/prepare-workspace.py")
	}
	_, err = docker(ctx, "", "build", "--build-arg", "BRIDGE_DIR=bin", "-t", managedImage, "-f", file, dir)
	return err
}

// Older managed desktops are upgraded in place so their writable layer, named
// volumes and published ports all survive the addition of the Xfce panels.
func (d localDesktop) ensureDesktop(ctx context.Context, cid string) error {
	stamp := filepath.Join(d.dir, "desktop-revision")
	want := cid + " " + desktopRevision
	if data, err := os.ReadFile(stamp); err == nil && string(data) == want {
		return nil
	}
	label, err := docker(ctx, "", "inspect", "--format", `{{index .Config.Labels "dev.bqbot.desktop"}}`, cid)
	if err != nil {
		return err
	}
	if label == desktopRevision {
		return atomicWorkspaceFile(stamp, want)
	}
	res, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return err
	}
	dir := filepath.Join(res, "workspace-image", "workspace")
	stage := func(src, dst string) error {
		if _, err := os.Stat(src); err != nil {
			return errors.New("缺少 Linux 工作区升级资源。开发版请运行 python3 -I scripts/prepare-workspace.py")
		}
		_, err := docker(ctx, "", "cp", src, cid+":"+dst)
		return err
	}
	for _, name := range []string{"install-desktop.sh", "workspace-browser.desktop", "panel.xml"} {
		if err := stage(filepath.Join(dir, name), "/opt/bqbot/"+name); err != nil {
			return err
		}
	}
	if _, err = docker(ctx, "", "exec", "--user", "0", cid, "sh", "/opt/bqbot/install-desktop.sh"); err != nil {
		return fmt.Errorf("安装 Linux 桌面组件失败：%w", err)
	}
	arch, err := docker(ctx, "", "exec", cid, "uname", "-m")
	if err != nil {
		return err
	}
	switch arch {
	case "aarch64":
		arch = "arm64"
	case "x86_64":
		arch = "amd64"
	default:
		return errors.New("不支持的 Linux 工作区架构")
	}
	const bin = "/usr/local/bin/bqbot-workspace"
	if err = stage(filepath.Join(dir, "bin", "bridge-"+arch), bin+".new"); err != nil {
		return err
	}
	if err = stage(filepath.Join(dir, "start.sh"), "/opt/bqbot/start.sh.new"); err != nil {
		return err
	}
	for _, dst := range []string{bin, "/opt/bqbot/start.sh"} {
		if _, err = docker(ctx, "", "exec", "--user", "0", cid, "mv", "-f", dst+".new", dst); err != nil {
			return err
		}
	}
	if _, err = docker(ctx, "", "restart", cid); err != nil {
		return err
	}
	return atomicWorkspaceFile(stamp, want)
}

// StartLocalWorkspace creates or resumes the bot's persistent Linux desktop.
func StartLocalWorkspace(ctx context.Context, st *store.Store, id string) (startErr error) {
	d, err := localFor(st, id)
	if err != nil {
		return err
	}
	cid, err := d.container(ctx)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(d.dir, 0700); err != nil {
		return err
	}
	token, err := d.credential()
	if err != nil {
		return err
	}
	if cid == "" {
		if err = workspaceImage(ctx); err != nil {
			return err
		}
		socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return err
		}
		port := strconv.Itoa(socket.LocalAddr().(*net.UDPAddr).Port)
		socket.Close()
		cid, err = docker(ctx, token, "create", "--name", d.name, "--label", "dev.bqbot.workspace="+d.name,
			"--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777", "--shm-size", "256m", "--security-opt", "no-new-privileges:true", "--stop-timeout", "20",
			"-p", "127.0.0.1::18443/tcp", "-p", "127.0.0.1:"+port+":"+port+"/udp",
			"-v", d.name+"-files:/home/agent/workspace", "-v", d.name+"-profile:/home/agent/profile",
			"-e", "BQBOT_WORKSPACE_TOKEN", "-e", "BQBOT_ICE_HOST=127.0.0.1", "-e", "BQBOT_MEDIA_PORT="+port, managedImage)
		if err != nil {
			return err
		}
	}
	running, err := docker(ctx, "", "inspect", "--format", "{{.State.Running}}", cid)
	if err != nil {
		return err
	}
	// Only roll back containers that this request started.
	if running != "true" {
		defer func() {
			if startErr != nil {
				cleanup, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()
				_, _ = docker(cleanup, "", "stop", cid)
			}
		}()
	}
	if _, err = docker(ctx, "", "start", cid); err != nil {
		return err
	}
	if err = d.ensureDesktop(ctx, cid); err != nil {
		return err
	}
	address, err := docker(ctx, "", "port", cid, "18443/tcp")
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return errors.New("容器没有发布本机 Bridge 端口")
	}
	cfg := store.ComputerConfig{URL: "http://127.0.0.1:" + port}
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}
	ready, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for {
		data, e := request(ready, cfg, token, "POST", "/mcp", body, 4096)
		var pong struct{ Result json.RawMessage }
		if e == nil && json.Unmarshal(data, &pong) == nil && len(pong.Result) > 0 {
			return atomicWorkspaceFile(filepath.Join(d.dir, "url"), cfg.URL)
		}
		select {
		case <-ready.Done():
			return fmt.Errorf("Linux 工作区未就绪，请检查 Docker 容器 %s", d.name)
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// StopLocalWorkspace keeps the container and its files for the next start.
func StopLocalWorkspace(ctx context.Context, st *store.Store, id string) error {
	d, err := localFor(st, id)
	if err != nil {
		return err
	}
	cid, err := d.container(ctx)
	if err != nil || cid == "" {
		return err
	}
	_, err = docker(ctx, "", "stop", cid)
	return err
}

// workspaceConnection resolves credentials only in the service process. A managed
// desktop can only send its token to the loopback port published by Docker.
func workspaceConnection(c store.ComputerConfig, st *store.Store) (store.ComputerConfig, string, error) {
	if err := c.Validate(); err != nil {
		return c, "", err
	}
	if !c.Enabled || c.Provider != "workspace" {
		return c, "", errors.New("Workspace Bridge is not enabled")
	}
	if !c.Managed {
		token := os.Getenv(c.TokenEnv)
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return c, "", errors.New("Workspace Bridge token environment variable is missing or invalid")
		}
		return c, token, nil
	}
	if st == nil {
		return c, "", errors.New("缺少本机工作区配置")
	}
	d, err := localFor(st, c.BotID)
	if err != nil {
		return c, "", err
	}
	data, err := os.ReadFile(filepath.Join(d.dir, "url"))
	if err != nil {
		return c, "", errors.New("请先启动此伙伴的 Linux 工作区")
	}
	u, err := url.Parse(string(data))
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return c, "", errors.New("无效的本机工作区地址")
	}
	secret, err := os.ReadFile(filepath.Join(d.dir, "token"))
	if err != nil {
		return c, "", err
	}
	c.URL = u.String()
	return c, string(secret), nil
}
