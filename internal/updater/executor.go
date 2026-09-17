package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var systemdXmeshPath = "/usr/local/bin/xmesh"
var systemdXrayPath = "/usr/local/lib/xmesh/xray"
var systemdService = "xmesh.service"
var systemdNodeConfigPath = "/etc/xmesh/node.json"

func prepare(ctx context.Context, c client, dir string, j *journal) error {
	if c.cfg.Mode == "systemd" {
		if err := copyFile(systemdXmeshPath, filepath.Join(dir, "old", "xmesh"), 0755); err != nil {
			return err
		}
		if _, err := os.Stat(systemdXrayPath); err == nil {
			if err := copyFile(systemdXrayPath, filepath.Join(dir, "old", "xray"), 0755); err != nil {
				return err
			}
		}
		return nil
	}
	container, err := composeContainer(ctx, c.cfg.InstallDir)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.Image}}", container)
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	j.OldImage = strings.TrimSpace(string(out))
	if j.OldImage == "" {
		return errors.New("old image missing")
	}
	if err := command(ctx, "", "docker", "tag", j.OldImage, "xmesh-rollback:"+j.Task.ID); err != nil {
		return err
	}
	for _, name := range []string{"compose.yaml", "image/xmesh", "image/xray", "image/Dockerfile"} {
		if err := copyFile(filepath.Join(c.cfg.InstallDir, name), filepath.Join(dir, "old", name), 0644); err != nil {
			return err
		}
	}
	imageDir := filepath.Join(dir, "new")
	if err := copyFile(filepath.Join(c.cfg.InstallDir, "image", "Dockerfile"), filepath.Join(imageDir, "Dockerfile"), 0644); err != nil {
		return err
	}
	return command(ctx, "", "docker", "build", "--tag", "xmesh-upgrade:"+j.Task.ID, imageDir)
}

func switchVersion(ctx context.Context, c client, dir string, j *journal) error {
	if c.cfg.Mode == "systemd" {
		if err := command(ctx, "", "systemctl", "stop", systemdService); err != nil {
			return err
		}
		if err := copyFile(filepath.Join(dir, "new", "xmesh"), systemdXmeshPath, 0755); err != nil {
			return err
		}
		if c.cfg.Role == "gateway" {
			if err := copyFile(filepath.Join(dir, "new", "xray"), systemdXrayPath, 0755); err != nil {
				return err
			}
		}
		return command(ctx, "", "systemctl", "start", systemdService)
	}
	old, err := os.ReadFile(filepath.Join(dir, "old", "compose.yaml"))
	if err != nil {
		return err
	}
	updated, err := composeImage(string(old), "xmesh-upgrade:"+j.Task.ID)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "new-compose.yaml"), []byte(updated), 0600); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(dir, "new-compose.yaml"), filepath.Join(c.cfg.InstallDir, "compose.yaml"), 0644); err != nil {
		return err
	}
	if err := command(ctx, c.cfg.InstallDir, "docker", "compose", "up", "-d", "--no-build", "--force-recreate"); err != nil {
		return err
	}
	return nil
}

func restore(ctx context.Context, c client, dir string, j *journal) error {
	if c.cfg.Mode == "systemd" {
		_ = command(ctx, "", "systemctl", "stop", systemdService)
		if err := copyFile(filepath.Join(dir, "old", "xmesh"), systemdXmeshPath, 0755); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, "old", "xray")); err == nil {
			if err := copyFile(filepath.Join(dir, "old", "xray"), systemdXrayPath, 0755); err != nil {
				return err
			}
		}
		return command(ctx, "", "systemctl", "start", systemdService)
	}
	old, err := os.ReadFile(filepath.Join(dir, "old", "compose.yaml"))
	if err != nil {
		return err
	}
	if j.OldImage == "" {
		return errors.New("old image ID missing")
	}
	rollbackCompose, err := composeImage(string(old), "xmesh-rollback:"+j.Task.ID)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "rollback-compose.yaml"), []byte(rollbackCompose), 0600); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(dir, "rollback-compose.yaml"), filepath.Join(c.cfg.InstallDir, "compose.yaml"), 0644); err != nil {
		return err
	}
	for _, name := range []string{"xmesh", "xray", "Dockerfile"} {
		mode := os.FileMode(0755)
		if name == "Dockerfile" {
			mode = 0644
		}
		if err := copyFile(filepath.Join(dir, "old", "image", name), filepath.Join(c.cfg.InstallDir, "image", name), mode); err != nil {
			return err
		}
	}
	if err := command(ctx, c.cfg.InstallDir, "docker", "compose", "up", "-d", "--no-build", "--force-recreate"); err != nil {
		return err
	}
	return copyFile(filepath.Join(dir, "old", "compose.yaml"), filepath.Join(c.cfg.InstallDir, "compose.yaml"), 0644)
}

func finishDocker(c Config, dir string) error {
	if c.Mode != "docker" {
		return nil
	}
	for _, name := range []string{"xmesh", "xray"} {
		if err := copyFile(filepath.Join(dir, "new", name), filepath.Join(c.InstallDir, "image", name), 0755); err != nil {
			return err
		}
	}
	return nil
}

func composeContainer(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "compose", "ps", "-q", "xmesh")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", errors.New("xmesh container missing")
	}
	return id, nil
}

func composeImage(source, image string) (string, error) {
	var lines []string
	found := false
	for _, line := range strings.Split(source, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "build:") {
			continue
		}
		if strings.HasPrefix(trim, "image:") {
			if found {
				return "", errors.New("multiple image directives")
			}
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines = append(lines, indent+"image: "+image)
			found = true
			continue
		}
		lines = append(lines, line)
	}
	if !found {
		return "", fmt.Errorf("Compose definition has no image directive")
	}
	return strings.Join(lines, "\n"), nil
}
