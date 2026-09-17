package controller

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestControllerUpgradeRequiresHostHelperAndQueuesOnce(t *testing.T) {
	server, _ := testServer(t, nil)
	dir := t.TempDir()
	server.cfg.StatePath = filepath.Join(dir, "controller-state.json")
	request := httptest.NewRequest(http.MethodPost, "/admin/controller/upgrade-latest", nil)
	response := httptest.NewRecorder()
	server.requestControllerUpgrade(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("missing host helper accepted: %d", response.Code)
	}
	if err := os.WriteFile(filepath.Join(dir, "controller-updater.ready"), []byte("ready\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated upgrade request was not redirected: %d", response.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "controller-upgrade.request")); !os.IsNotExist(err) {
		t.Fatalf("unauthenticated request queued an upgrade: %v", err)
	}
	response = httptest.NewRecorder()
	server.requestControllerUpgrade(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("upgrade not queued: %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "controller-upgrade.request")); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	server.requestControllerUpgrade(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate upgrade accepted: %d", response.Code)
	}
	if got := server.controllerUpgradeStatus().State; got != "queued" {
		t.Fatalf("status = %q, want queued", got)
	}
}
