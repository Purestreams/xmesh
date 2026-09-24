package controller

import (
	"errors"
	"sort"
	"strings"
	"time"

	"xmesh/internal/model"
)

const usageRetention = 30 * 24 * time.Hour

var errGrantLinkMismatch = errors.New("grant Link does not belong to grant route")

type usageBytes struct {
	Upload   uint64 `json:"upload"`
	Download uint64 `json:"download"`
}

type usageLinkView struct {
	ID      string                `json:"id"`
	Name    string                `json:"name"`
	Windows map[string]usageBytes `json:"windows"`
}

type usageUserView struct {
	UserID  string                `json:"user_id"`
	Windows map[string]usageBytes `json:"windows"`
	Links   []usageLinkView       `json:"links"`
}

var usageWindows = []struct {
	name string
	span time.Duration
}{{"5h", 5 * time.Hour}, {"1d", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}, {"30d", usageRetention}}

// Counters start at zero for each Gateway process. On a new process, count
// its first report as traffic since startup; repeated reports contribute only
// their monotonic delta.
func sampleGrantUsage(state *model.State, instanceID, grantID string, links []model.GrantLinkUsage, now time.Time) error {
	grant := state.Grants[grantID]
	attachment := state.Attachments[grant.AttachmentID]
	names := map[string]string{}
	for id, link := range state.Links {
		if link.AttachmentID == grant.AttachmentID {
			names[id] = link.Name
		}
	}
	for id, link := range state.RetiredLinks {
		if link.AttachmentID == grant.AttachmentID && now.Before(link.ExpiresAt) {
			names[id] = link.Name
		}
	}
	if attachment.UpstreamID != "" {
		names[attachment.ID] = state.Upstreams[attachment.UpstreamID].Name
	}
	return sampleGrantUsageFor(state, instanceID, grantID, grant.UserID, names, links, now)
}

func sampleRetiredGrantUsage(state *model.State, instanceID, grantID string, retired model.RetiredGrant, links []model.GrantLinkUsage, now time.Time) error {
	return sampleGrantUsageFor(state, instanceID, grantID, retired.UserID, retired.LinkNames, links, now)
}

func sampleGrantUsageFor(state *model.State, instanceID, grantID, userID string, names map[string]string, links []model.GrantLinkUsage, now time.Time) error {
	if state.UsageCounters == nil {
		state.UsageCounters = map[string]model.UsageCounter{}
	}
	if state.UsageHistory == nil {
		state.UsageHistory = map[string][]model.UsageBucket{}
	}
	if state.UsageLabels == nil {
		state.UsageLabels = map[string]string{}
	}
	for _, link := range links {
		name, ok := names[link.LinkID]
		if !ok {
			return errGrantLinkMismatch
		}
		counterKey := grantID + "/" + link.LinkID
		usageKey := userID + "/" + link.LinkID
		previous, known := state.UsageCounters[counterKey]
		state.UsageCounters[counterKey] = model.UsageCounter{InstanceID: instanceID, UploadBytes: link.UploadBytes, DownloadBytes: link.DownloadBytes}
		var up, down uint64
		switch {
		case !known || previous.InstanceID != instanceID:
			up, down = link.UploadBytes, link.DownloadBytes
		case link.UploadBytes < previous.UploadBytes || link.DownloadBytes < previous.DownloadBytes:
			continue
		default:
			up, down = link.UploadBytes-previous.UploadBytes, link.DownloadBytes-previous.DownloadBytes
		}
		if up == 0 && down == 0 {
			continue
		}
		if name != "" {
			state.UsageLabels[usageKey] = name
		}
		bucketAt := now.Truncate(historyInterval)
		buckets := state.UsageHistory[usageKey]
		if len(buckets) == 0 || !buckets[len(buckets)-1].At.Equal(bucketAt) {
			buckets = append(buckets, model.UsageBucket{At: bucketAt})
		}
		buckets[len(buckets)-1].UploadBytes += up
		buckets[len(buckets)-1].DownloadBytes += down
		state.UsageHistory[usageKey] = buckets
	}
	return nil
}

func pruneUsageHistory(state *model.State, now time.Time) {
	cutoff := now.Add(-usageRetention)
	for key, buckets := range state.UsageHistory {
		first := 0
		for first < len(buckets) && buckets[first].At.Before(cutoff) {
			first++
		}
		if first == len(buckets) {
			delete(state.UsageHistory, key)
			delete(state.UsageLabels, key)
		} else {
			state.UsageHistory[key] = buckets[first:]
		}
	}
}

func usageViews(state model.State, now time.Time) []usageUserView {
	users := map[string]*usageUserView{}
	for _, user := range sortedUsers(state) {
		users[user.ID] = &usageUserView{UserID: user.ID, Windows: map[string]usageBytes{}, Links: []usageLinkView{}}
	}
	for _, user := range users {
		linkIDs := map[string]bool{}
		for _, grant := range state.Grants {
			if grant.UserID != user.UserID {
				continue
			}
			for _, link := range state.Links {
				if link.AttachmentID == grant.AttachmentID {
					linkIDs[link.ID] = true
				}
			}
			if attachment := state.Attachments[grant.AttachmentID]; attachment.UpstreamID != "" {
				linkIDs[attachment.ID] = true
			}
		}
		for key := range state.UsageHistory {
			if strings.HasPrefix(key, user.UserID+"/") {
				linkIDs[strings.TrimPrefix(key, user.UserID+"/")] = true
			}
		}
		for linkID := range linkIDs {
			key := user.UserID + "/" + linkID
			buckets := state.UsageHistory[key]
			name := state.UsageLabels[key]
			if link, ok := state.Links[linkID]; ok {
				name = link.Name
			}
			if attachment, ok := state.Attachments[linkID]; ok && attachment.UpstreamID != "" {
				name = state.Upstreams[attachment.UpstreamID].Name
			}
			if name == "" {
				name = linkID
			}
			entry := usageLinkView{ID: linkID, Name: name, Windows: map[string]usageBytes{}}
			for _, window := range usageWindows {
				for _, bucket := range buckets {
					if bucket.At.Before(now.Add(-window.span)) {
						continue
					}
					linkSum := entry.Windows[window.name]
					linkSum.Upload += bucket.UploadBytes
					linkSum.Download += bucket.DownloadBytes
					entry.Windows[window.name] = linkSum
					userSum := user.Windows[window.name]
					userSum.Upload += bucket.UploadBytes
					userSum.Download += bucket.DownloadBytes
					user.Windows[window.name] = userSum
				}
			}
			user.Links = append(user.Links, entry)
		}
	}
	result := make([]usageUserView, 0, len(users))
	for _, user := range users {
		sort.Slice(user.Links, func(i, j int) bool { return user.Links[i].Name < user.Links[j].Name })
		result = append(result, *user)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UserID < result[j].UserID })
	return result
}
