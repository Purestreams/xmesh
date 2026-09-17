package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"xmesh/internal/identity"
	"xmesh/internal/model"
)

type Config struct {
	ControllerURL string `json:"controller_url"`
	NodeID        string `json:"node_id"`
	Role          string `json:"role"`
	Credential    string `json:"credential"`
	Mode          string `json:"mode"`
	InstallDir    string `json:"install_dir,omitempty"`
	WorkDir       string `json:"work_dir,omitempty"`
}

func validateConfig(c Config) error {
	if !strings.HasPrefix(c.ControllerURL, "https://") || c.NodeID == "" || c.Credential == "" || (c.Role != "gateway" && c.Role != "agent") {
		return errors.New("invalid updater identity")
	}
	if c.Mode != "systemd" && c.Mode != "docker" {
		return errors.New("updater mode must be systemd or docker")
	}
	if c.Mode == "docker" && (c.InstallDir == "" || !filepath.IsAbs(c.InstallDir)) {
		return errors.New("Docker install directory must be absolute")
	}
	return nil
}

func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, validateConfig(c)
}

func SaveConfig(path string, c Config) error {
	if err := validateConfig(c); err != nil {
		return err
	}
	if c.WorkDir == "" {
		if c.Mode == "docker" {
			c.WorkDir = filepath.Join(c.InstallDir, "updater", "jobs")
		} else {
			c.WorkDir = "/var/lib/xmesh-updater/jobs"
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".updater-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

type client struct {
	cfg          Config
	http         *http.Client
	localArchive string
}

func (c client) request(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.cfg.ControllerURL, "/")+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Credential)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return resp.StatusCode, fmt.Errorf("controller %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil && resp.StatusCode == 200 {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

type journal struct {
	Task        model.UpgradeTask `json:"task"`
	Phase       string            `json:"phase"`
	Result      string            `json:"result,omitempty"`
	Reported    bool              `json:"reported,omitempty"`
	Local       bool              `json:"local,omitempty"`
	Error       string            `json:"error,omitempty"`
	OldImage    string            `json:"old_image,omitempty"`
	OldInstance string            `json:"old_instance,omitempty"`
	SwitchAt    time.Time         `json:"switch_at,omitempty"`
}

var errStatusUnavailable = errors.New("controller status temporarily unavailable")

func Run(ctx context.Context, cfg Config, version string) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if runtime.GOOS != "linux" {
		return errors.New("host updater requires Linux")
	}
	if cfg.WorkDir == "" {
		if cfg.Mode == "docker" {
			cfg.WorkDir = filepath.Join(cfg.InstallDir, "updater", "jobs")
		} else {
			cfg.WorkDir = "/var/lib/xmesh-updater/jobs"
		}
	}
	if err := os.MkdirAll(cfg.WorkDir, 0700); err != nil {
		return err
	}
	unlock, err := lock(filepath.Join(cfg.WorkDir, "updater.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	c := client{cfg: cfg, http: &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Controller redirect refused") }}}
	_ = heartbeat(ctx, c, version)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			if err := heartbeat(ctx, c, version); err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "updater heartbeat:", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	if err := recoverJobs(ctx, c); err != nil {
		return err
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if err := tick(ctx, c); err != nil {
			fmt.Fprintln(os.Stderr, "updater:", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

var localVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([-._][A-Za-z0-9._-]+)?$`)

// LocalUpgrade lets a verified manual installer reuse the same durable switch,
// health check and rollback path without creating a Controller task.
func LocalUpgrade(ctx context.Context, cfg Config, target, archive, expectedHash string) error {
	return localUpgrade(ctx, cfg, target, archive, expectedHash, &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Controller redirect refused") }})
}

func localUpgrade(ctx context.Context, cfg Config, target, archive, expectedHash string, httpClient *http.Client) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if runtime.GOOS != "linux" || !localVersionPattern.MatchString(target) || len(expectedHash) != 64 || !filepath.IsAbs(archive) {
		return errors.New("invalid local upgrade parameters")
	}
	if cfg.WorkDir == "" {
		if cfg.Mode == "docker" {
			cfg.WorkDir = filepath.Join(cfg.InstallDir, "updater", "jobs")
		} else {
			cfg.WorkDir = "/var/lib/xmesh-updater/jobs"
		}
	}
	if err := os.MkdirAll(cfg.WorkDir, 0700); err != nil {
		return err
	}
	unlock, err := lock(filepath.Join(cfg.WorkDir, "updater.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	client := client{cfg: cfg, http: httpClient, localArchive: archive}
	if err := recoverJobs(ctx, client); err != nil {
		return err
	}
	old, err := nodeStatus(ctx, client)
	if err != nil {
		return err
	}
	if old.NodeID != cfg.NodeID || old.Role != model.Role(cfg.Role) || old.BinaryVersion == "" || old.LastSeen.IsZero() || time.Since(old.LastSeen) > 45*time.Second || old.LastSeen.After(time.Now().Add(5*time.Second)) {
		return errors.New("node has no fresh version status; manual upgrade refused")
	}
	id, err := identity.Token(12)
	if err != nil {
		return err
	}
	reservation := model.UpgradeTask{ID: id, Manual: true, NodeID: cfg.NodeID, Role: model.Role(cfg.Role), FromVersion: old.BinaryVersion, TargetVersion: target, SHA256: strings.ToLower(expectedHash), Mode: cfg.Mode, Arch: runtime.GOARCH, Attempt: 1}
	if err := saveJournal(filepath.Join(cfg.WorkDir, id), journal{Task: reservation, Phase: "reserving", Local: true}); err != nil {
		return err
	}
	var task model.UpgradeTask
	_, err = client.request(ctx, "POST", "/api/v1/updater/manual", manualRequest(reservation), &task)
	if err != nil {
		return err
	}
	if task.ID != id || task.NodeID != cfg.NodeID || task.Role != model.Role(cfg.Role) || !task.Manual || task.FromVersion != old.BinaryVersion || task.TargetVersion != target || task.Mode != cfg.Mode || task.Arch != runtime.GOARCH || task.SHA256 != strings.ToLower(expectedHash) {
		return errors.New("controller returned a mismatched manual upgrade task")
	}
	return execute(ctx, client, task)
}

func manualRequest(task model.UpgradeTask) map[string]string {
	return map[string]string{"id": task.ID, "from_version": task.FromVersion, "target_version": task.TargetVersion, "sha256": task.SHA256, "mode": task.Mode, "arch": task.Arch}
}

func tick(ctx context.Context, c client) error {
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return errors.New("unsupported architecture")
	}
	var task model.UpgradeTask
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	status, err := c.request(reqCtx, "GET", "/api/v1/updater/task", nil, &task)
	cancel()
	if err != nil {
		return err
	}
	if status == 204 {
		return nil
	}
	if task.NodeID != c.cfg.NodeID || task.Role != model.Role(c.cfg.Role) || task.Mode != c.cfg.Mode || task.Arch != arch || task.Attempt < 1 || task.SHA256 == "" {
		return errors.New("task does not match this installation")
	}
	return execute(ctx, c, task)
}

func heartbeat(ctx context.Context, c client, version string) error {
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err := c.request(reqCtx, "POST", "/api/v1/updater/heartbeat", map[string]any{"mode": c.cfg.Mode, "arch": runtime.GOARCH, "version": version, "protocol": 1}, nil)
	return err
}

func report(ctx context.Context, c client, j journal, stage, detail string) error {
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err := c.request(reqCtx, "POST", "/api/v1/updater/task/"+j.Task.ID+"/report", map[string]any{"stage": stage, "error": detail, "attempt": j.Task.Attempt}, nil)
	return err
}

func reportTerminal(ctx context.Context, c client, dir string, j *journal) error {
	if j.Reported {
		return nil
	}
	if err := report(ctx, c, *j, j.Result, j.Error); err != nil {
		return err
	}
	j.Reported = true
	return saveJournal(dir, *j)
}

func saveJournal(dir string, j journal) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".journal-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, "journal.json")); err != nil {
		return err
	}
	return syncDir(dir)
}

func command(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".xmesh-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dst))
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func nodeConfigPath(c Config) string {
	if c.Mode == "docker" {
		return filepath.Join(c.InstallDir, "config", "node.json")
	}
	return systemdNodeConfigPath
}
func nodeCredential(c Config) (string, error) {
	var v struct {
		Credential string `json:"credential"`
	}
	b, err := os.ReadFile(nodeConfigPath(c))
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return "", err
	}
	if v.Credential == "" {
		return "", errors.New("node credential missing")
	}
	return v.Credential, nil
}
func nodeStatus(ctx context.Context, c client) (model.NodeStatus, error) {
	var st model.NodeStatus
	cred, err := nodeCredential(c.cfg)
	if err != nil {
		return st, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", strings.TrimSuffix(c.cfg.ControllerURL, "/")+"/api/v1/self/status", nil)
	if err != nil {
		return st, err
	}
	req.Header.Set("Authorization", "Bearer "+cred)
	resp, err := c.http.Do(req)
	if err != nil {
		return st, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return st, fmt.Errorf("node status %s", resp.Status)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&st)
	return st, err
}

func recoverJobs(ctx context.Context, c client) error {
	entries, err := os.ReadDir(c.cfg.WorkDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(c.cfg.WorkDir, entry.Name())
		b, err := os.ReadFile(filepath.Join(dir, "journal.json"))
		if err != nil {
			continue
		}
		var j journal
		if err := json.Unmarshal(b, &j); err != nil {
			return err
		}
		if j.Task.NodeID != c.cfg.NodeID {
			return errors.New("foreign job in updater directory")
		}
		if j.Phase == "reserving" && j.Task.Manual {
			var registered model.UpgradeTask
			status, err := c.request(ctx, "POST", "/api/v1/updater/manual", manualRequest(j.Task), &registered)
			if err != nil && status != http.StatusConflict {
				return err
			}
			if err == nil {
				if registered.ID != j.Task.ID || !registered.Manual || registered.NodeID != c.cfg.NodeID {
					return errors.New("manual reservation recovery mismatch")
				}
				j.Task = registered
			}
			j.Phase, j.Result, j.Error = "done", "failed", "manual installer stopped before switching"
			j.Reported = status == http.StatusConflict
			if err := saveJournal(dir, j); err != nil {
				return err
			}
		}
		if j.Phase == "verifying" {
			if err := resumeVerification(ctx, c, dir, &j); err != nil && !errors.Is(err, errStatusUnavailable) {
				return err
			}
		} else if j.Phase == "switching" || j.Phase == "rolling_back" {
			recoveryCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			err := rollback(recoveryCtx, c, dir, &j)
			cancel()
			if err != nil && j.Result == "manual_intervention" {
				return err
			}
		}
		if (j.Phase == "downloading" || j.Phase == "prepared") && j.Result == "" {
			j.Phase = "done"
			j.Result = "failed"
			j.Error = "updater interrupted before switching"
			if err := saveJournal(dir, j); err != nil {
				return err
			}
		}
		if j.Result != "" && !j.Reported {
			if err := reportTerminal(ctx, c, dir, &j); err != nil {
				fmt.Fprintln(os.Stderr, "report pending:", err)
			}
		}
	}
	return nil
}

func execute(ctx context.Context, c client, task model.UpgradeTask) error {
	dir := filepath.Join(c.cfg.WorkDir, task.ID)
	if filepath.Base(task.ID) != task.ID || task.ID == "." || task.ID == ".." {
		return errors.New("invalid task ID")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "journal.json")); err == nil {
		var j journal
		if err := json.Unmarshal(b, &j); err != nil {
			return err
		}
		if j.Task.ID != task.ID || j.Task.Attempt != task.Attempt {
			return errors.New("task journal mismatch")
		}
		if j.Phase == "reserving" && j.Task.Manual && task.Manual {
			// The reservation was persisted before the Controller request.
			// The returned task can now use the normal durable execution path.
		} else {
			if j.Result != "" {
				return reportTerminal(ctx, c, dir, &j)
			}
			if j.Phase == "verifying" {
				return resumeVerification(ctx, c, dir, &j)
			}
			return errors.New("unfinished job requires recovery")
		}
	}
	j := journal{Task: task, Phase: "downloading", Local: c.localArchive != ""}
	if err := saveJournal(dir, j); err != nil {
		return err
	}
	old, err := nodeStatus(ctx, c)
	if err != nil {
		return failBeforeSwitch(ctx, c, dir, &j, err)
	}
	if task.FromVersion != "" && old.BinaryVersion != task.FromVersion {
		return failBeforeSwitch(ctx, c, dir, &j, fmt.Errorf("node version changed from %s to %s", task.FromVersion, old.BinaryVersion))
	}
	j.OldInstance = old.InstanceID
	if err := saveJournal(dir, j); err != nil {
		return err
	}
	_ = report(ctx, c, j, "downloading", "")
	if err := download(ctx, c, dir, task); err != nil {
		return failBeforeSwitch(ctx, c, dir, &j, err)
	}
	if err := prepare(ctx, c, dir, &j); err != nil {
		return failBeforeSwitch(ctx, c, dir, &j, err)
	}
	j.Phase = "prepared"
	if err := saveJournal(dir, j); err != nil {
		return err
	}
	_ = report(ctx, c, j, "prepared", "")
	j.Phase = "switching"
	j.SwitchAt = time.Now().UTC()
	if err := saveJournal(dir, j); err != nil {
		return err
	}
	_ = report(ctx, c, j, "switching", "")
	if err := switchVersion(ctx, c, dir, &j); err != nil {
		return rollbackWithCause(ctx, c, dir, &j, err)
	}
	j.Phase = "verifying"
	if err := saveJournal(dir, j); err != nil {
		return rollbackWithCause(ctx, c, dir, &j, err)
	}
	_ = report(ctx, c, j, "verifying", "")
	return resumeVerification(ctx, c, dir, &j)
}

func resumeVerification(ctx context.Context, c client, dir string, j *journal) error {
	if err := verify(ctx, c, *j); err != nil {
		if errors.Is(err, errStatusUnavailable) {
			_ = report(ctx, c, *j, "uncertain", err.Error())
			return err
		}
		return rollbackWithCause(ctx, c, dir, j, err)
	}
	if err := finishDocker(c.cfg, dir); err != nil {
		return rollbackWithCause(ctx, c, dir, j, err)
	}
	j.Result = "succeeded"
	j.Phase = "done"
	if err := saveJournal(dir, *j); err != nil {
		return err
	}
	return reportTerminal(ctx, c, dir, j)
}

func failBeforeSwitch(ctx context.Context, c client, dir string, j *journal, cause error) error {
	j.Phase = "done"
	j.Result = "failed"
	j.Error = cause.Error()
	if err := saveJournal(dir, *j); err != nil {
		return err
	}
	_ = reportTerminal(ctx, c, dir, j)
	return cause
}

func rollbackWithCause(ctx context.Context, c client, dir string, j *journal, cause error) error {
	j.Error = cause.Error()
	recoveryCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return rollback(recoveryCtx, c, dir, j)
}

func rollback(ctx context.Context, c client, dir string, j *journal) error {
	j.Phase = "rolling_back"
	if err := saveJournal(dir, *j); err != nil {
		return err
	}
	_ = report(ctx, c, *j, "rolling_back", j.Error)
	err := restore(ctx, c, dir, j)
	if err == nil {
		err = verifyRollback(ctx, c, *j)
	}
	if err != nil {
		j.Result = "manual_intervention"
		j.Error = strings.TrimSpace(j.Error + "; rollback: " + err.Error())
	} else {
		j.Result = "rolled_back"
	}
	j.Phase = "done"
	if saveErr := saveJournal(dir, *j); saveErr != nil {
		return saveErr
	}
	_ = reportTerminal(ctx, c, dir, j)
	if err != nil {
		return err
	}
	return fmt.Errorf("upgrade rolled back: %s", j.Error)
}

func verify(ctx context.Context, c client, j journal) error {
	deadline := time.Now().Add(90 * time.Second)
	var stable time.Time
	var lastErr error
	badFreshStatus := false
	for time.Now().Before(deadline) {
		if err := serviceRunning(ctx, c.cfg); err != nil {
			return err
		}
		st, err := nodeStatus(ctx, c)
		if err != nil {
			lastErr = err
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
			continue
		}
		if st.LastSeen.After(j.SwitchAt) && (st.BinaryVersion != j.Task.TargetVersion || st.ApplyError != "" || st.XrayError != "") {
			badFreshStatus = true
		}
		if st.InstanceID != "" && st.InstanceID != j.OldInstance && st.BinaryVersion == j.Task.TargetVersion && st.LastSeen.After(j.SwitchAt) && st.AppliedVersion >= st.DesiredVersion && st.ApplyError == "" && st.XrayError == "" && (c.cfg.Role != "gateway" || st.XrayReady && st.Ready) {
			if stable.IsZero() {
				stable = time.Now()
			}
			if time.Since(stable) >= 30*time.Second {
				return nil
			}
		} else {
			stable = time.Time{}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	if lastErr != nil && !badFreshStatus {
		return fmt.Errorf("%w: %v", errStatusUnavailable, lastErr)
	}
	return errors.New("new process did not report healthy for 30 seconds")
}

func verifyRollback(ctx context.Context, c client, j journal) error {
	deadline := time.Now().Add(60 * time.Second)
	controllerUnavailable := false
	for time.Now().Before(deadline) {
		if err := serviceRunning(ctx, c.cfg); err == nil {
			st, err := nodeStatus(ctx, c)
			if err != nil {
				controllerUnavailable = true
			}
			if err == nil && st.BinaryVersion == j.Task.FromVersion && st.LastSeen.After(j.SwitchAt) && st.AppliedVersion >= st.DesiredVersion && st.ApplyError == "" && st.XrayError == "" && (c.cfg.Role != "gateway" || st.XrayReady) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	if controllerUnavailable && serviceRunning(ctx, c.cfg) == nil {
		version, err := localVersion(ctx, c.cfg)
		if err == nil && version == j.Task.FromVersion {
			return nil
		}
	}
	return errors.New("old version did not recover")
}

func localVersion(ctx context.Context, c Config) (string, error) {
	var cmd *exec.Cmd
	if c.Mode == "systemd" {
		cmd = exec.CommandContext(ctx, systemdXmeshPath, "version")
	} else {
		id, err := composeContainer(ctx, c.InstallDir)
		if err != nil {
			return "", err
		}
		cmd = exec.CommandContext(ctx, "docker", "exec", id, "/usr/local/bin/xmesh", "version")
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func serviceRunning(ctx context.Context, c Config) error {
	if c.Mode == "systemd" {
		return command(ctx, "", "systemctl", "is-active", "--quiet", systemdService)
	}
	cmd := exec.CommandContext(ctx, "docker", "compose", "ps", "-q", "xmesh")
	cmd.Dir = c.InstallDir
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return errors.New("xmesh container missing")
	}
	inspect := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", id)
	state, err := inspect.Output()
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(state)) != "true" {
		return errors.New("xmesh container not running")
	}
	return nil
}

func download(ctx context.Context, c client, dir string, task model.UpgradeTask) error {
	if task.Asset != fmt.Sprintf("xmesh-%s-linux-%s.tar.gz", task.TargetVersion, task.Arch) || len(task.SHA256) != 64 {
		return errors.New("invalid release asset")
	}
	var source io.ReadCloser
	if c.localArchive != "" {
		local, err := os.Open(c.localArchive)
		if err != nil {
			return err
		}
		source = local
	} else {
		url := strings.TrimSuffix(c.cfg.ControllerURL, "/") + "/releases/" + task.TargetVersion + "/" + task.Asset
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return fmt.Errorf("release download: %s", resp.Status)
		}
		source = resp.Body
	}
	defer source.Close()
	file, err := os.Create(filepath.Join(dir, "release.tar.gz"))
	if err != nil {
		return err
	}
	defer file.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, h), io.LimitReader(source, (256<<20)+1))
	if err != nil {
		return err
	}
	if n > 256<<20 {
		return errors.New("archive too large")
	}
	if hex.EncodeToString(h.Sum(nil)) != task.SHA256 {
		return errors.New("archive SHA256 mismatch")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	stage := filepath.Join(dir, "new")
	if err := os.MkdirAll(stage, 0700); err != nil {
		return err
	}
	if err := command(ctx, "", "tar", "-xzf", file.Name(), "-C", stage, "xmesh", "xray"); err != nil {
		return err
	}
	for _, name := range []string{"xmesh", "xray"} {
		info, err := os.Stat(filepath.Join(stage, name))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("invalid archive member")
		}
	}
	cmd := exec.CommandContext(ctx, filepath.Join(stage, "xmesh"), "version")
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != task.TargetVersion {
		return errors.New("archive binary version mismatch")
	}
	return nil
}
