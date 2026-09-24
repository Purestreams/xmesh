package gateway

import (
	"context"
	"strings"
	"time"

	statscommand "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Xray counters are reset on each read and folded into the Gateway process
// counters, so Controller receives the same monotonic shape as Agent traffic.
func (r *Runtime) collectXrayStats(ctx context.Context) {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	if !r.xrayReady.Load() || r.local.Gateway.StatsListen == "" {
		return
	}
	r.mu.RLock()
	config := r.xrayStatsConfig
	r.mu.RUnlock()
	if len(config.Upstreams) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(r.local.Gateway.StatsListen, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		r.logger.Warn("connect Xray stats", "error", err)
		return
	}
	defer conn.Close()
	response, err := statscommand.NewStatsServiceClient(conn).QueryStats(ctx, &statscommand.QueryStatsRequest{Pattern: "user>>>", Reset_: true})
	if err != nil {
		r.logger.Warn("query Xray stats", "error", err)
		return
	}
	attachments := map[string]bool{}
	for _, upstream := range config.Upstreams {
		attachments[upstream.AttachmentID] = true
	}
	grants := map[string]string{}
	for _, grant := range config.Grants {
		if attachments[grant.AttachmentID] {
			grants["grant-"+grant.ID] = grant.AttachmentID
		}
	}
	for _, stat := range response.GetStat() {
		parts := strings.Split(stat.GetName(), ">>>")
		if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" || stat.GetValue() <= 0 {
			continue
		}
		attachmentID, ok := grants[parts[1]]
		if !ok {
			continue
		}
		grantID := strings.TrimPrefix(parts[1], "grant-")
		both, route := r.grantCounters(grantID), r.grantLinkCounters(grantID, attachmentID)
		switch parts[3] {
		case "uplink":
			both.upload.Add(uint64(stat.GetValue()))
			route.upload.Add(uint64(stat.GetValue()))
		case "downlink":
			both.download.Add(uint64(stat.GetValue()))
			route.download.Add(uint64(stat.GetValue()))
		}
	}
}
