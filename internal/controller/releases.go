package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxReleaseAsset = 256 << 20
const maxReleaseManifest = 64 << 10

var releaseVersionPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

type releaseStatus struct {
	Version   string
	GitHubURL string
	LocalURL  string
	Enabled   bool
	Ready     bool
	Assets    []releaseAssetStatus
}

type releaseAssetStatus struct {
	Name  string
	Ready bool
}

func releaseAssets(version string) []string {
	return []string{
		"SHA256SUMS", "install.sh", "install-docker.sh", "install-controller.sh",
		"xmesh-" + version + "-linux-amd64.tar.gz",
		"xmesh-" + version + "-linux-arm64.tar.gz",
		"xmesh-" + version + "-windows-amd64.exe",
	}
}

func (s *Server) releaseDirectory() string {
	return filepath.Join(s.cfg.ReleaseDir, s.cfg.ReleaseVersion)
}

func (s *Server) releaseStatus() releaseStatus {
	version := s.cfg.ReleaseVersion
	status := releaseStatus{Version: version, GitHubURL: strings.TrimSuffix(s.cfg.ReleaseBaseURL, "/") + "/" + version,
		LocalURL: strings.TrimSuffix(s.cfg.PublicURL, "/") + "/releases/" + version,
		Enabled:  s.releaseEnabled(), Ready: true}
	if !status.Enabled {
		status.Ready = false
		return status
	}
	for _, name := range releaseAssets(version) {
		ready := s.validCachedAsset(name) == nil
		status.Assets = append(status.Assets, releaseAssetStatus{Name: name, Ready: ready})
		if !ready {
			status.Ready = false
		}
	}
	return status
}

func (s *Server) releaseEnabled() bool {
	u, err := url.Parse(s.cfg.ReleaseBaseURL)
	return s.cfg.ReleaseDir != "" && releaseVersionPattern.MatchString(s.cfg.ReleaseVersion) && err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func (s *Server) releaseExpectedHash(name string) (string, error) {
	manifest, err := os.ReadFile(filepath.Join(s.releaseDirectory(), "SHA256SUMS"))
	if err != nil {
		return "", err
	}
	hashes, err := parseReleaseManifest(s.cfg.ReleaseVersion, manifest)
	if err != nil {
		return "", err
	}
	hash, ok := hashes[name]
	if !ok {
		return "", errors.New("asset missing from SHA256SUMS")
	}
	return hash, nil
}

func (s *Server) validCachedAsset(name string) error {
	if !s.releaseEnabled() || !isReleaseAsset(s.cfg.ReleaseVersion, name) {
		return errors.New("release asset not configured")
	}
	if name == "SHA256SUMS" {
		manifest, err := os.ReadFile(filepath.Join(s.releaseDirectory(), "SHA256SUMS"))
		if err != nil {
			return err
		}
		_, err = parseReleaseManifest(s.cfg.ReleaseVersion, manifest)
		return err
	}
	expected, err := s.releaseExpectedHash(name)
	if err != nil {
		return err
	}
	file, err := os.Open(filepath.Join(s.releaseDirectory(), name))
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return errors.New("cached asset checksum mismatch")
	}
	return nil
}

func isReleaseAsset(version, name string) bool {
	for _, asset := range releaseAssets(version) {
		if name == asset {
			return true
		}
	}
	return false
}

func (s *Server) releaseAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("asset")
	if !s.releaseEnabled() || r.PathValue("version") != s.cfg.ReleaseVersion || !isReleaseAsset(s.cfg.ReleaseVersion, name) {
		http.NotFound(w, r)
		return
	}
	if err := s.ensureReleaseAsset(r.Context(), name); err != nil {
		s.logger.Error("fetch release asset", "asset", name, "error", err)
		http.Error(w, "release asset unavailable; retry or use GitHub directly", http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600, immutable")
	http.ServeFile(w, r, filepath.Join(s.releaseDirectory(), name))
}

func (s *Server) ensureReleaseAsset(ctx context.Context, name string) error {
	if s.validCachedAsset(name) == nil {
		return nil
	}
	s.releaseMu.Lock()
	defer s.releaseMu.Unlock()
	if name != "SHA256SUMS" && s.validCachedAsset("SHA256SUMS") != nil {
		if err := s.fetchReleaseAsset(ctx, "SHA256SUMS", "", maxReleaseManifest); err != nil {
			return err
		}
	}
	if s.validCachedAsset(name) == nil {
		return nil
	}
	if name == "SHA256SUMS" {
		return s.fetchReleaseAsset(ctx, name, "", maxReleaseManifest)
	}
	expected, err := s.releaseExpectedHash(name)
	if err != nil {
		return err
	}
	return s.fetchReleaseAsset(ctx, name, expected, maxReleaseAsset)
}

func (s *Server) fetchReleaseAsset(ctx context.Context, name, expected string, limit int64) error {
	if err := os.MkdirAll(s.releaseDirectory(), 0750); err != nil {
		return err
	}
	assetURL := strings.TrimSuffix(s.cfg.ReleaseBaseURL, "/") + "/" + s.cfg.ReleaseVersion + "/" + name
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{}
	if s.releaseHTTPClient != nil {
		clone := *s.releaseHTTPClient
		client = &clone
	}
	client.Timeout = 10 * time.Minute
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many release redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("release redirect must use HTTPS")
		}
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("release source returned %s", response.Status)
	}
	if response.ContentLength > limit {
		return errors.New("release asset exceeds size limit")
	}
	tmp, err := os.CreateTemp(s.releaseDirectory(), ".release-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(response.Body, limit+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > limit {
		return errors.New("release asset exceeds size limit")
	}
	if expected != "" && hex.EncodeToString(hash.Sum(nil)) != expected {
		return errors.New("release asset checksum mismatch")
	}
	if name == "SHA256SUMS" {
		manifest, err := os.ReadFile(tmp.Name())
		if err != nil {
			return err
		}
		if _, err := parseReleaseManifest(s.cfg.ReleaseVersion, manifest); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), filepath.Join(s.releaseDirectory(), name))
}

func parseReleaseManifest(version string, manifest []byte) (map[string]string, error) {
	if len(manifest) > maxReleaseManifest {
		return nil, errors.New("release manifest too large")
	}
	hashes := make(map[string]string)
	for _, line := range strings.Split(string(manifest), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 || len(parts[0]) != 64 || !isReleaseAsset(version, parts[1]) || parts[1] == "SHA256SUMS" {
			return nil, errors.New("invalid release manifest entry")
		}
		if _, err := hex.DecodeString(parts[0]); err != nil {
			return nil, errors.New("invalid release manifest digest")
		}
		if _, exists := hashes[parts[1]]; exists {
			return nil, errors.New("duplicate release manifest entry")
		}
		hashes[parts[1]] = strings.ToLower(parts[0])
	}
	for _, asset := range releaseAssets(version)[1:] {
		if _, ok := hashes[asset]; !ok {
			return nil, fmt.Errorf("release manifest missing %s", asset)
		}
	}
	return hashes, nil
}
