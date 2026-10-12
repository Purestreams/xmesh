package controller

import (
	"fmt"
	"net/http"

	"xmesh/internal/model"
)

func applyIngressSwitches(r *http.Request, gateway *model.Gateway) {
	if !r.Form.Has("ingress_controls") {
		return
	}
	gateway.VMessDisabled = r.FormValue("vmess_enabled") != "1"
	gateway.HY2Disabled = r.FormValue("hy2_enabled") != "1"
}

func (s *Server) saveGatewayIngress(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if r.FormValue("ingress_controls") != "1" {
		http.Error(w, "ingress controls required", http.StatusBadRequest)
		return
	}
	err := s.store.Update(func(state *model.State) error {
		gateway, ok := state.Gateways[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("gateway not found")
		}
		previous := gateway
		applyIngressSwitches(r, &gateway)
		port := gateway.HY2Port
		if !gateway.HY2Disabled && port == 0 {
			port = gateway.VMessPort
			if port == 0 {
				port = 8080
			}
		}
		if err := configureHY2(state, &gateway, port, s.now()); err != nil {
			return err
		}
		state.Gateways[gateway.ID] = gateway
		if previous.VMessDisabled != gateway.VMessDisabled || previous.HY2Disabled != gateway.HY2Disabled || previous.HY2Port != gateway.HY2Port {
			bumpGateway(state, gateway.ID)
			for id, grant := range state.Grants {
				if state.Attachments[grant.AttachmentID].GatewayID == gateway.ID {
					grant.Published = false
					state.Grants[id] = grant
				}
			}
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
