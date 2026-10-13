package controller

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xmesh/internal/hy2"
	"xmesh/internal/identity"
	"xmesh/internal/model"
)

func configureHY2(state *model.State, gateway *model.Gateway, port int, now time.Time) error {
	if port < 0 || port > 65535 {
		return fmt.Errorf("HY2 UDP port must be between 0 and 65535")
	}
	gateway.HY2Port = port
	if !gateway.HY2Enabled() {
		return nil
	}
	if gateway.HY2Certificate == "" {
		cert, key, pin, err := hy2.Generate(gateway.PublicHost, now)
		if err != nil {
			return err
		}
		gateway.HY2Certificate, gateway.HY2PrivateKey, gateway.HY2PinSHA256 = cert, key, pin
	}
	if err := hy2.Validate(gateway.HY2Certificate, gateway.HY2PrivateKey, gateway.HY2PinSHA256, gateway.PublicHost, now); err != nil {
		return err
	}
	for id, grant := range state.Grants {
		if state.Attachments[grant.AttachmentID].GatewayID == gateway.ID && grant.HY2Auth == "" {
			secret, err := identity.Token(32)
			if err != nil {
				return err
			}
			grant.HY2Auth = secret
			state.Grants[id] = grant
		}
	}
	return nil
}

func parseHY2Port(r *http.Request, fallback int) (int, error) {
	if err := r.ParseForm(); err != nil {
		return 0, err
	}
	if !r.Form.Has("hy2_port") || r.FormValue("hy2_port") == "" {
		return fallback, nil
	}
	port, err := strconv.Atoi(r.FormValue("hy2_port"))
	if err != nil || port < 0 || port > 65535 {
		return 0, fmt.Errorf("HY2 UDP port must be between 0 and 65535")
	}
	return port, nil
}

func hy2Share(gateway model.Gateway, grant model.Grant, name string) string {
	query := url.Values{"sni": {strings.Trim(gateway.PublicHost, "[]")}, "pinSHA256": {gateway.HY2PinSHA256}}
	return (&url.URL{Scheme: "hysteria2", User: url.User(grant.HY2Auth), Host: net.JoinHostPort(strings.Trim(gateway.PublicHost, "[]"), strconv.Itoa(gateway.HY2Port)), Path: "/", RawQuery: query.Encode(), Fragment: name + " / HY2"}).String()
}

// The official client requires both CA trust and pin verification when
// insecure is false. Include only the public certificate, never the key.
func (s *Server) hy2ClientBundle(w http.ResponseWriter, r *http.Request, state model.State, userID string) {
	grant, ok := state.Grants[r.URL.Query().Get("grant")]
	attachment := state.Attachments[grant.AttachmentID]
	gateway := state.Gateways[attachment.GatewayID]
	if !ok || grant.UserID != userID || !state.Users[userID].Enabled || !grant.Enabled || !grant.Published || !subscriptionRouteAvailable(&state, attachment, s.now()) || !gateway.Enabled || !gateway.HY2Enabled() || !state.NodeStatus[gateway.ID].HY2Ready || grant.HY2Auth == "" {
		http.Error(w, "HY2 authorization unavailable", http.StatusForbidden)
		return
	}
	if err := hy2.Validate(gateway.HY2Certificate, gateway.HY2PrivateKey, gateway.HY2PinSHA256, gateway.PublicHost, s.now()); err != nil {
		http.Error(w, "HY2 certificate unavailable", http.StatusConflict)
		return
	}
	config := map[string]any{
		"server": hy2Share(gateway, grant, routeDisplayName(state, attachment)),
		"tls":    map[string]any{"ca": "gateway.pem"},
		"socks5": map[string]any{"listen": "127.0.0.1:1080"},
	}
	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		http.Error(w, "cannot encode HY2 client", 500)
		return
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, file := range []struct {
		name string
		data []byte
	}{
		{"client.json", payload}, {"gateway.pem", []byte(gateway.HY2Certificate)},
		{"README.txt", []byte("Run from this directory: hysteria client -c client.json\nTLS verifies the trusted certificate, server name, validity period and SHA-256 pin.\nKeep the subscription and client.json private.\n")},
	} {
		writer, err := archive.Create(file.name)
		if err != nil {
			http.Error(w, "cannot create HY2 bundle", 500)
			return
		}
		if _, err := writer.Write(file.data); err != nil {
			http.Error(w, "cannot create HY2 bundle", 500)
			return
		}
	}
	if err := archive.Close(); err != nil {
		http.Error(w, "cannot create HY2 bundle", 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="xmesh-hy2.zip"`)
	_, _ = w.Write(buffer.Bytes())
}
