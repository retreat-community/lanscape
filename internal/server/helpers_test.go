package server

import "github.com/retreat-community/lanscape/internal/topo"

type topoSegment = topo.Segment

func members(ids ...string) []topo.Member {
	var out []topo.Member
	for _, id := range ids {
		out = append(out, topo.Member{Node: id, Iface: "eth0", Speed: 2500})
	}
	return out
}

func pathResult(src, dst string, mbps uint64) topo.PathResult {
	return topo.PathResult{Seg: 0, SegID: "lan", Src: src, Dst: dst, SrcIf: "eth0", DstIf: "eth0", BestBPS: mbps * 1_000_000}
}
