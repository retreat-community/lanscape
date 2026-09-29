package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/retreat-community/lanscape/internal/proto"
)

// agentRequest answers requests an agent sends on behalf of a local user (LuCI).
func (s *Server) agentRequest(ctx context.Context, agentID string, c *fullConn, env proto.Envelope) {
	var reply any
	var err error
	switch env.Type {
	case proto.MsgRunRequest:
		var req proto.RunRequestMsg
		_ = json.Unmarshal(env.Data, &req)
		if req.Kind != KindFull {
			req.Kind = KindReachability
		}
		var id int64
		id, err = s.runner.Start(ctx, RunOptions{Kind: req.Kind, Nodes: []string{agentID}, Actor: "agent:" + s.agentName(agentID)})
		reply = map[string]int64{"id": id}
	case proto.MsgRunSummary:
		reply, err = s.runSummary(ctx, agentID)
	}
	rtype := env.Type + "_result"
	if err != nil {
		rtype, reply = proto.MsgError, map[string]string{"error": err.Error()}
	}
	out, e := proto.NewEnvelope(rtype, env.ID, reply)
	if e == nil {
		_ = c.send(ctx, out)
	}
}

// runSummary extracts the paths of one agent from the newest finished run.
func (s *Server) runSummary(ctx context.Context, agentID string) (proto.RunSummaryMsg, error) {
	runs, err := s.store.RunReports(ctx, []string{KindFull, KindReachability}, 20)
	if err != nil {
		return proto.RunSummaryMsg{}, err
	}
	for _, run := range runs {
		var rep Report
		if json.Unmarshal(run.Report, &rep) != nil {
			continue
		}
		sum := proto.RunSummaryMsg{RunID: run.ID, Status: rep.Status, Finished: rep.Finished, Paths: []proto.PathSummary{},
			Problems: []string{}}
		for _, p := range rep.Paths {
			peer := ""
			switch agentID {
			case p.Src:
				peer = p.Dst
			case p.Dst:
				peer = p.Src
			default:
				continue
			}
			ps := proto.PathSummary{Peer: s.agentName(peer), Segment: p.SegID, Mbps: float64(p.BestBPS) / 1e6, Verdict: p.Verdict}
			if p.Ping != nil {
				ps.RTTMS = float64(p.Ping.RTTAvgUS) / 1000
			}
			sum.Paths = append(sum.Paths, ps)
		}
		for _, pr := range rep.Problems {
			if pr.Src == agentID || pr.Dst == agentID {
				sum.Problems = append(sum.Problems, fmt.Sprintf("%s: %s", pr.Kind, pr.Detail))
			}
		}
		if len(sum.Paths) > 0 {
			return sum, nil
		}
	}
	return proto.RunSummaryMsg{Paths: []proto.PathSummary{}, Problems: []string{}}, nil
}
