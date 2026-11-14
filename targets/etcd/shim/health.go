package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var healthClient = &http.Client{Timeout: 500 * time.Millisecond}

// health asks etcd for this member's role, the leader's name and the term.
// It tries the v3 gateway first (etcd 3.x), then the v2 stats API (etcd 2.x).
// It never fails: if etcd is not up yet the state is "starting".
func health(ctx context.Context, c Config, etcd string) healthInfo {
	if h, ok := healthV3(ctx, etcd); ok {
		return h
	}
	if h, ok := healthV2(ctx, etcd); ok {
		return h
	}
	return healthInfo{state: "starting"}
}

func postJSON(ctx context.Context, url string, out any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	return doJSON(req, out)
}

func getJSON(ctx context.Context, url string, out any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	return doJSON(req, out)
}

func doJSON(req *http.Request, out any) bool {
	resp, err := healthClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(out) == nil
}

func healthV3(ctx context.Context, etcd string) (healthInfo, bool) {
	var st struct {
		Header struct {
			MemberID string `json:"member_id"`
		} `json:"header"`
		Leader   string `json:"leader"`
		RaftTerm string `json:"raftTerm"`
	}
	if !postJSON(ctx, "http://"+etcd+"/v3/maintenance/status", &st) || st.Header.MemberID == "" {
		return healthInfo{}, false
	}
	var ml struct {
		Members []struct {
			ID   string `json:"ID"`
			Name string `json:"name"`
		} `json:"members"`
	}
	names := map[string]string{}
	if postJSON(ctx, "http://"+etcd+"/v3/cluster/member/list", &ml) {
		for _, m := range ml.Members {
			names[m.ID] = m.Name
		}
	}
	h := healthInfo{state: "follower", leader: names[st.Leader]}
	switch st.Leader {
	case "", "0":
		h.state = "candidate"
	case st.Header.MemberID:
		h.state = "leader"
	}
	h.term, _ = strconv.ParseUint(st.RaftTerm, 10, 64)
	return h, true
}

func healthV2(ctx context.Context, etcd string) (healthInfo, bool) {
	var self struct {
		ID         string `json:"id"`
		State      string `json:"state"`
		LeaderInfo struct {
			Leader string `json:"leader"`
		} `json:"leaderInfo"`
	}
	if !getJSON(ctx, "http://"+etcd+"/v2/stats/self", &self) || self.ID == "" {
		return healthInfo{}, false
	}
	var ml struct {
		Members []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"members"`
	}
	names := map[string]string{}
	if getJSON(ctx, "http://"+etcd+"/v2/members", &ml) {
		for _, m := range ml.Members {
			names[m.ID] = m.Name
		}
	}
	state := strings.ToLower(strings.TrimPrefix(self.State, "State"))
	if state == "" {
		state = "follower"
	}
	return healthInfo{state: state, leader: names[self.LeaderInfo.Leader]}, true
}
