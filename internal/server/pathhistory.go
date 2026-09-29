package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/retreat-community/lanscape/internal/topo"
)

// PathPoint is one measurement of a path in a past run.
type PathPoint struct {
	RunID    int64  `json:"run_id"`
	Finished int64  `json:"finished"`
	SrcIf    string `json:"src_if"`
	DstIf    string `json:"dst_if"`
	BestBPS  uint64 `json:"best_bps"`
	RTTUS    uint32 `json:"rtt_us,omitempty"`
	Verdict  string `json:"verdict"`
	Status   string `json:"status,omitempty"` // echo status when the path failed
}

// apiPathHistory returns the history of the paths between two nodes (optionally in one
// segment), oldest first, from the newest full and reachability runs.
func (s *Server) apiPathHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	src, dst, seg := q.Get("src"), q.Get("dst"), q.Get("seg")
	if src == "" || dst == "" {
		writeError(w, http.StatusBadRequest, "src and dst are required")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	runs, err := s.store.RunReports(r.Context(), []string{KindFull, KindReachability}, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []PathPoint{}
	for i := len(runs) - 1; i >= 0; i-- {
		var rep struct {
			Paths []topo.PathResult `json:"paths"`
		}
		if json.Unmarshal(runs[i].Report, &rep) != nil {
			continue
		}
		for _, p := range rep.Paths {
			same := (p.Src == src && p.Dst == dst) || (p.Src == dst && p.Dst == src)
			if !same || (seg != "" && p.SegID != seg) {
				continue
			}
			pt := PathPoint{RunID: runs[i].ID, Finished: runs[i].Finished, SrcIf: p.SrcIf, DstIf: p.DstIf, BestBPS: p.BestBPS,
				Verdict: p.Verdict}
			if p.Ping != nil {
				pt.RTTUS = p.Ping.RTTAvgUS
			}
			if p.Echo != nil && p.Echo.Status != "ok" {
				pt.Status = p.Echo.Status
			}
			out = append(out, pt)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
