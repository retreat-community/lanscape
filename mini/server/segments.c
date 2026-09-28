/* Groups agent interfaces into segments by subnet and VLAN. */
#include "server.h"
#include "../common/util.h"

#include <string.h>

uint32_t expect_for(uint32_t net, int prefix)
{
    int i, best = -1;
    uint32_t mbps = 0;
    for (i = 0; i < g_conf.nexpect; i++) {
        const expect_t *e = &g_conf.expect[i];
        /* the configured network must contain the segment */
        if (e->prefix <= prefix && (net & ls_prefix_mask(e->prefix)) == e->net && e->prefix > best) {
            best = e->prefix;
            mbps = e->mbps;
        }
    }
    return mbps;
}

int build_segments(const agent_info *const *agents, int nagents, segment_t *segs, int max)
{
    int n = 0, a, i, s;
    for (a = 0; a < nagents; a++) {
        for (i = 0; i < agents[a]->nifs; i++) {
            const s_if *f = &agents[a]->ifs[i];
            uint32_t net;
            segment_t *seg = NULL;
            if (!f->addr || f->prefix == 0 || f->prefix > 30 || !(f->flags & IFF_LS_UP))
                continue;
            net = f->addr & ls_prefix_mask(f->prefix);
            for (s = 0; s < n; s++) {
                segment_t *c = &segs[s];
                if (c->net == net && c->prefix == f->prefix &&
                    (c->vlan == f->vlan || c->vlan == 0 || f->vlan == 0)) {
                    seg = c;
                    break;
                }
            }
            if (!seg) {
                if (n >= max)
                    continue;
                seg = &segs[n++];
                memset(seg, 0, sizeof(*seg));
                seg->net = net;
                seg->prefix = f->prefix;
                seg->manual_mbps = expect_for(net, f->prefix);
            }
            if (!seg->vlan && f->vlan)
                seg->vlan = f->vlan;
            if (seg->nmem < MAX_AGENTS) {
                seg->mem[seg->nmem].agent = a;
                seg->mem[seg->nmem].ifi = i;
                seg->nmem++;
            }
        }
    }
    return n;
}

void seg_name(const segment_t *s, char *out, size_t cap)
{
    if (s->vlan)
        ls_fmt(out, cap, "%I/%d vlan %u", s->net, s->prefix, s->vlan);
    else
        ls_fmt(out, cap, "%I/%d", s->net, s->prefix);
}
