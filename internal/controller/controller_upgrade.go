package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type controllerUpgradeView struct {
	Ready   bool
	State   string
	Version string
	At      string
}

func (s *Server) controllerUpgradeDir() string {
	return filepath.Dir(s.cfg.StatePath)
}

func (s *Server) controllerUpgradeStatus() controllerUpgradeView {
	dir := s.controllerUpgradeDir()
	view := controllerUpgradeView{Version: s.cfg.ReleaseVersion}
	if _, err := os.Stat(filepath.Join(dir, "controller-updater.ready")); err != nil {
		return view
	}
	view.Ready = true
	var reported struct {
		State   string `json:"state"`
		Version string `json:"version"`
		At      string `json:"at"`
	}
	if payload, err := os.ReadFile(filepath.Join(dir, "controller-upgrade.status.json")); err == nil {
		if json.Unmarshal(payload, &reported) == nil {
			view.State, view.At = reported.State, reported.At
			if reported.Version != "" {
				view.Version = reported.Version
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "controller-upgrade.running")); err == nil {
		view.State = "running"
	} else if _, err := os.Stat(filepath.Join(dir, "controller-upgrade.request")); err == nil {
		view.State = "queued"
	}
	return view
}

func (s *Server) requestControllerUpgrade(w http.ResponseWriter, r *http.Request) {
	status := s.controllerUpgradeStatus()
	if !status.Ready {
		http.Error(w, "host updater is not installed or active", http.StatusConflict)
		return
	}
	if status.State == "queued" || status.State == "running" {
		http.Error(w, "Controller upgrade is already queued or running", http.StatusConflict)
		return
	}
	path := filepath.Join(s.controllerUpgradeDir(), "controller-upgrade.request")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		http.Error(w, "Controller upgrade is already queued", http.StatusConflict)
		return
	}
	if err != nil {
		s.logger.Error("queue Controller upgrade", "error", err)
		http.Error(w, "unable to queue Controller upgrade", http.StatusInternalServerError)
		return
	}
	_, writeErr := file.WriteString(s.now().UTC().Format(time.RFC3339) + "\n")
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		http.Error(w, "unable to queue Controller upgrade", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
