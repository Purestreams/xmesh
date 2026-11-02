package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	handlercommand "github.com/xtls/xray-core/app/proxyman/command"
	routercommand "github.com/xtls/xray-core/app/router/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/infra/conf"
	hy2account "github.com/xtls/xray-core/proxy/hysteria/account"
	"github.com/xtls/xray-core/proxy/vmess"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type xrayUserAddition struct {
	tag  string
	user *protocol.User
}

type xrayAddition struct {
	users     []xrayUserAddition
	outbounds []xrayOutbound
	routing   xrayRouting
}

// Only extend the running configuration. Revocations, credential rotations,
// transport changes and replacements still use the restart path, which also
// terminates connections authenticated with the removed credentials.
func planXrayAddition(previous, next []byte) (*xrayAddition, error) {
	var old, desired xrayConfig
	if err := json.Unmarshal(previous, &old); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(next, &desired); err != nil {
		return nil, err
	}
	if old.API == nil || !slices.Contains(old.API.Services, "HandlerService") || !slices.Contains(old.API.Services, "RoutingService") {
		return nil, nil
	}
	plan := &xrayAddition{routing: desired.Routing}
	if len(old.Inbounds) != len(desired.Inbounds) {
		return nil, nil
	}
	for i, inbound := range old.Inbounds {
		candidate := desired.Inbounds[i]
		if inbound.Protocol == "vmess" || inbound.Protocol == "hysteria" {
			clients := make(map[string]xrayClient, len(candidate.Settings.Clients))
			for _, client := range candidate.Settings.Clients {
				if _, duplicate := clients[client.Email]; duplicate || client.Email == "" {
					return nil, nil
				}
				clients[client.Email] = client
			}
			for _, client := range inbound.Settings.Clients {
				if clients[client.Email] != client {
					return nil, nil
				}
				delete(clients, client.Email)
			}
			for _, client := range candidate.Settings.Clients {
				if _, added := clients[client.Email]; !added {
					continue
				}
				user := &protocol.User{Email: client.Email}
				if inbound.Protocol == "vmess" {
					user.Account = serial.ToTypedMessage(&vmess.Account{Id: client.ID})
				} else {
					user.Account = serial.ToTypedMessage(&hy2account.Account{Auth: client.Auth})
				}
				plan.users = append(plan.users, xrayUserAddition{tag: candidate.Tag, user: user})
			}
			inbound.Settings.Clients, candidate.Settings.Clients = nil, nil
		}
		if !reflect.DeepEqual(inbound, candidate) {
			return nil, nil
		}
	}
	remaining := make(map[string]xrayOutbound, len(desired.Outbounds))
	for _, outbound := range desired.Outbounds {
		remaining[outbound.Tag] = outbound
	}
	for _, outbound := range old.Outbounds {
		if !reflect.DeepEqual(outbound, remaining[outbound.Tag]) {
			return nil, nil
		}
		delete(remaining, outbound.Tag)
	}
	for _, outbound := range desired.Outbounds {
		if _, added := remaining[outbound.Tag]; added {
			plan.outbounds = append(plan.outbounds, outbound)
		}
	}
	// Keep existing rules in the same relative order, including the deny rules
	// preceding user routes. A replacement must not masquerade as an addition.
	index := 0
	for _, rule := range desired.Routing.Rules {
		if index < len(old.Routing.Rules) && reflect.DeepEqual(rule, old.Routing.Rules[index]) {
			index++
		}
	}
	if index != len(old.Routing.Rules) || old.Routing.DomainStrategy != desired.Routing.DomainStrategy {
		return nil, nil
	}
	old.Inbounds, desired.Inbounds = nil, nil
	old.Outbounds, desired.Outbounds = nil, nil
	old.Routing, desired.Routing = xrayRouting{}, xrayRouting{}
	if !reflect.DeepEqual(old, desired) {
		return nil, nil
	}
	return plan, nil
}

func (r *Runtime) hotAddXrayConfig(ctx context.Context, previous, next []byte) (bool, error) {
	plan, err := planXrayAddition(previous, next)
	if err != nil || plan == nil || r.local.Gateway.StatsListen == "" {
		return false, err
	}
	// Build every request before making changes. An RPC or persistence failure
	// falls back to a restart so partial application is never acknowledged.
	payload, err := json.Marshal(struct {
		Outbounds []xrayOutbound `json:"outbounds"`
		Routing   xrayRouting    `json:"routing"`
	}{plan.outbounds, plan.routing})
	if err != nil {
		return false, err
	}
	var config conf.Config
	if err := json.Unmarshal(payload, &config); err != nil {
		return false, err
	}
	outbounds := make([]*handlercommand.AddOutboundRequest, 0, len(config.OutboundConfigs))
	for _, outbound := range config.OutboundConfigs {
		built, err := outbound.Build()
		if err != nil {
			return false, err
		}
		outbounds = append(outbounds, &handlercommand.AddOutboundRequest{Outbound: built})
	}
	routing, err := config.RouterConfig.Build()
	if err != nil {
		return false, err
	}
	if err := writeProtectedAtomic(r.local.Gateway.XrayConfigPath, next); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(r.local.Gateway.StatsListen, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false, err
	}
	defer conn.Close()
	handlers := handlercommand.NewHandlerServiceClient(conn)
	for _, outbound := range outbounds {
		if _, err := handlers.AddOutbound(ctx, outbound); err != nil {
			return false, fmt.Errorf("add Xray outbound: %w", err)
		}
	}
	// Install routing before accepting the new credentials. Existing rules are
	// retained and replaced atomically by Xray's RoutingService.
	if _, err := routercommand.NewRoutingServiceClient(conn).AddRule(ctx, &routercommand.AddRuleRequest{Config: serial.ToTypedMessage(routing)}); err != nil {
		return false, fmt.Errorf("update Xray routing: %w", err)
	}
	for _, addition := range plan.users {
		if _, err := handlers.AlterInbound(ctx, &handlercommand.AlterInboundRequest{Tag: addition.tag, Operation: serial.ToTypedMessage(&handlercommand.AddUserOperation{User: addition.user})}); err != nil {
			return false, fmt.Errorf("add Xray user: %w", err)
		}
	}
	return true, nil
}
