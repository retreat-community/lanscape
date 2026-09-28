/* "Check all" run: serial execution of tests over every path, verdicts and problems. */
#include "server.h"
#include "../common/jw.h"
#include "../common/tlv.h"
#include "../common/util.h"

#include <stdlib.h>
#include <string.h>

enum { SK_PING, SK_ECHO, SK_PMTU, SK_JUMBO, SK_TCP1, SK_TCPN };
enum { ST_SKIPPED = 250, ST_OFFLINE = 251, ST_PENDING = 255 };

typedef struct {
    uint8_t st;
    uint64_t bps;
    uint16_t lcpu;
    uint16_t pcpu;
    uint8_t path_ok;
} tcp_r;

typedef struct {
    int seg;
    int a, ai, b, bi;
    uint8_t ping_st;
    uint16_t sent, recv;
    uint32_t rmin, ravg, rmax;
    uint8_t echo_st;
    uint32_t echo_rtt;
    uint8_t pmtu_st;
    uint8_t jumbo_st;
    uint16_t jumbo_size;
    tcp_r tcp[2];
    char route_dev[16];
    uint32_t expected;
    uint64_t best;
    const char *verdict;
    int mismatch;
} path_t;

typedef struct {
    uint8_t kind;
    int path;
} step_t;

static struct {
    int running;
    uint32_t id;
    uint64_t started;
    int cancelled;
    int nag;
    agent_info *ag;
    int nseg;
    segment_t *segs;
    int npath;
    path_t *paths;
    int nstep;
    step_t *steps;
    int cur;
    int phase; /* 0 idle, 1 waiting grant ack, 2 waiting result */
    uint64_t deadline;
    uint32_t cmd_id;
    uint32_t grant_id;
    uint8_t token[LSTP_TOKEN_LEN];
    uint32_t streams;
    uint32_t duration_ms;
} R;

static const char *st_name(uint8_t st)
{
    static const char *n[] = {"ok", "route_mismatch", "unreachable", "refused", "protocol",
                              "auth_fail", "timeout", "counter_mismatch", "no_device",
                              "unsupported", "internal", "busy", "msgsize", "limit"};
    if (st < sizeof(n) / sizeof(n[0]))
        return n[st];
    if (st == ST_SKIPPED)
        return "skipped";
    if (st == ST_OFFLINE)
        return "offline";
    return "pending";
}

static const char *kind_name(uint8_t k)
{
    static const char *n[] = {"ping", "tcp echo", "mtu 1500", "mtu jumbo", "tcp x1", "tcp xN"};
    return n[k];
}

int run_active(void) { return R.running; }

static void free_run(void)
{
    free(R.ag);
    free(R.segs);
    free(R.paths);
    free(R.steps);
    R.ag = NULL;
    R.segs = NULL;
    R.paths = NULL;
    R.steps = NULL;
}

static uint32_t step_cost_ms(const step_t *s)
{
    switch (s->kind) {
    case SK_PING:
        return g_conf.ping_count * 100 + 200;
    case SK_ECHO:
        return 300;
    case SK_PMTU:
    case SK_JUMBO:
        return 800;
    default:
        return R.duration_ms + LSTP_WARMUP_MS + 500;
    }
}

static void progress(void)
{
    jw w;
    char buf[160];
    size_t len;
    char *s;
    uint64_t eta = 0;
    int i;
    for (i = R.cur; i < R.nstep; i++)
        eta += step_cost_ms(&R.steps[i]);
    jw_init(&w);
    jw_obj(&w);
    jw_kuint(&w, "run", R.id);
    jw_kint(&w, "done", R.cur);
    jw_kint(&w, "total", R.nstep);
    jw_kuint(&w, "eta_s", eta / 1000);
    if (R.cur < R.nstep) {
        const path_t *p = &R.paths[R.steps[R.cur].path];
        char seg[48];
        seg_name(&R.segs[p->seg], seg, sizeof(seg));
        ls_fmt(buf, sizeof(buf), "%s %s -> %s (%s)", kind_name(R.steps[R.cur].kind), R.ag[p->a].id,
               R.ag[p->b].id, seg);
        jw_kstr(&w, "step", buf);
    }
    jw_end_obj(&w);
    s = jw_take(&w, &len);
    if (s) {
        sse_broadcast("progress", s, len);
        free(s);
    }
}

static int send_cmd(agent_t *a, const path_t *p, uint8_t kind)
{
    uint8_t buf[512];
    const s_if *fa = &R.ag[p->a].ifs[p->ai];
    const s_if *fb = &R.ag[p->b].ifs[p->bi];
    ls_wr w;
    size_t n;
    R.cmd_id++;
    ls_wr_init(&w, buf, sizeof(buf));
    ls_frame_begin(&w, LC_CMD);
    ls_put_u32(&w, T_CMD_ID, R.cmd_id);
    ls_put_str(&w, T_SRC_DEV, fa->name);
    ls_put(&w, T_SRC_ADDR, &fa->addr, 4);
    ls_put(&w, T_DST_ADDR, &fb->addr, 4);
    ls_put_u16(&w, T_DST_PORT, R.ag[p->b].data_port);
    switch (kind) {
    case SK_PING:
        ls_put_u8(&w, T_CMD_KIND, CK_PING);
        ls_put_u16(&w, T_COUNT, (uint16_t)g_conf.ping_count);
        ls_put_u16(&w, T_INTERVAL_MS, 100);
        ls_put_u16(&w, T_SIZE, 84);
        break;
    case SK_PMTU:
    case SK_JUMBO:
        ls_put_u8(&w, T_CMD_KIND, CK_PMTU);
        ls_put_u16(&w, T_COUNT, 3);
        ls_put_u16(&w, T_INTERVAL_MS, 200);
        ls_put_u16(&w, T_SIZE, kind == SK_PMTU ? 1500 : p->jumbo_size);
        break;
    default:
        ls_put_u8(&w, T_CMD_KIND, CK_LSTP);
        ls_put_u32(&w, T_RUN_ID, R.grant_id);
        ls_put(&w, T_RUN_TOKEN, R.token, sizeof(R.token));
        ls_put_u8(&w, T_TEST_KIND, kind == SK_ECHO ? TK_ECHO : TK_TCP_THROUGHPUT);
        ls_put_u8(&w, T_DIRECTION, DIR_FORWARD);
        ls_put_u8(&w, T_STREAMS, (uint8_t)(kind == SK_TCPN ? R.streams : 1));
        ls_put_u32(&w, T_DURATION_MS, kind == SK_ECHO ? 0 : R.duration_ms);
        ls_put_u16(&w, T_COUNT, 10);
    }
    n = ls_frame_end(&w);
    return n ? agent_send(a, buf, n) : -1;
}

static int send_grant(agent_t *b)
{
    uint8_t buf[128];
    ls_wr w;
    size_t n;
    ls_random(&R.grant_id, sizeof(R.grant_id));
    ls_random(R.token, sizeof(R.token));
    ls_wr_init(&w, buf, sizeof(buf));
    ls_frame_begin(&w, LC_GRANT);
    ls_put_u32(&w, T_RUN_ID, R.grant_id);
    ls_put(&w, T_RUN_TOKEN, R.token, sizeof(R.token));
    ls_put_u32(&w, T_TTL_S, LSTP_TOKEN_TTL_S);
    n = ls_frame_end(&w);
    return n ? agent_send(b, buf, n) : -1;
}

static void set_status(path_t *p, uint8_t kind, uint8_t st)
{
    switch (kind) {
    case SK_PING: p->ping_st = st; break;
    case SK_ECHO: p->echo_st = st; break;
    case SK_PMTU: p->pmtu_st = st; break;
    case SK_JUMBO: p->jumbo_st = st; break;
    case SK_TCP1: p->tcp[0].st = st; break;
    default: p->tcp[1].st = st;
    }
}

static void finish(void);

static void advance(void)
{
    while (R.cur < R.nstep && !R.cancelled) {
        const step_t *s = &R.steps[R.cur];
        path_t *p = &R.paths[s->path];
        agent_t *a = agent_by_id(R.ag[p->a].id);
        agent_t *b = agent_by_id(R.ag[p->b].id);
        if ((s->kind == SK_TCP1 || s->kind == SK_TCPN) && p->echo_st != ST_OK) {
            set_status(p, s->kind, ST_SKIPPED);
            R.cur++;
            continue;
        }
        if (s->kind == SK_TCPN && p->tcp[0].st != ST_OK && p->tcp[0].st != ST_COUNTER_MISMATCH) {
            /* no point in more streams when a single stream moved no data */
            set_status(p, s->kind, ST_SKIPPED);
            R.cur++;
            continue;
        }
        if (!a || a->fd < 0 || !b || b->fd < 0) {
            set_status(p, s->kind, ST_OFFLINE);
            R.cur++;
            continue;
        }
        progress();
        if (s->kind == SK_ECHO || s->kind == SK_TCP1 || s->kind == SK_TCPN) {
            if (send_grant(b)) {
                set_status(p, s->kind, ST_OFFLINE);
                R.cur++;
                continue;
            }
            R.phase = 1;
            R.deadline = ls_now_us() + 5000000u;
            return;
        }
        if (send_cmd(a, p, s->kind)) {
            set_status(p, s->kind, ST_OFFLINE);
            R.cur++;
            continue;
        }
        R.phase = 2;
        R.deadline = ls_now_us() + (uint64_t)(step_cost_ms(s) + 5000) * 1000u;
        return;
    }
    finish();
}

static void store_result(const uint8_t *pl, size_t plen)
{
    const step_t *s = &R.steps[R.cur];
    path_t *p = &R.paths[s->path];
    uint8_t st = ST_INTERNAL;
    ls_get_u8(pl, plen, T_STATUS, &st);
    set_status(p, s->kind, st);
    if (st == ST_ROUTE_MISMATCH)
        ls_get_str(pl, plen, T_ROUTE_DEV, p->route_dev, sizeof(p->route_dev));
    if (s->kind == SK_PING) {
        ls_get_u16(pl, plen, T_SENT, &p->sent);
        ls_get_u16(pl, plen, T_RECV, &p->recv);
        ls_get_u32(pl, plen, T_RTT_MIN_US, &p->rmin);
        ls_get_u32(pl, plen, T_RTT_AVG_US, &p->ravg);
        ls_get_u32(pl, plen, T_RTT_MAX_US, &p->rmax);
    } else if (s->kind == SK_ECHO) {
        ls_get_u32(pl, plen, T_RTT_AVG_US, &p->echo_rtt);
    } else if (s->kind == SK_TCP1 || s->kind == SK_TCPN) {
        tcp_r *t = &p->tcp[s->kind == SK_TCPN];
        ls_get_u64(pl, plen, T_BPS, &t->bps);
        ls_get_u16(pl, plen, T_LOCAL_CPU, &t->lcpu);
        ls_get_u16(pl, plen, T_PEER_CPU, &t->pcpu);
        t->path_ok = 1;
        ls_get_u8(pl, plen, T_PATH_OK, &t->path_ok);
    }
}

void run_on_frame(agent_t *a, uint8_t type, const uint8_t *pl, size_t plen)
{
    const step_t *s;
    const path_t *p;
    if (!R.running || R.cur >= R.nstep)
        return;
    s = &R.steps[R.cur];
    p = &R.paths[s->path];
    if (type == LC_GRANT_ACK && R.phase == 1) {
        uint32_t id = 0;
        agent_t *src;
        if (!ls_get_u32(pl, plen, T_RUN_ID, &id) || id != R.grant_id || strcmp(a->info.id, R.ag[p->b].id))
            return;
        src = agent_by_id(R.ag[p->a].id);
        if (!src || src->fd < 0 || send_cmd(src, p, s->kind)) {
            R.deadline = 0;
            return;
        }
        R.phase = 2;
        R.deadline = ls_now_us() + (uint64_t)(step_cost_ms(s) + 45000) * 1000u;
    } else if (type == LC_CMD_RESULT && R.phase == 2) {
        uint32_t id = 0;
        if (!ls_get_u32(pl, plen, T_CMD_ID, &id) || id != R.cmd_id || strcmp(a->info.id, R.ag[p->a].id))
            return;
        store_result(pl, plen);
        R.phase = 0;
        R.cur++;
        advance();
    }
}

void run_on_disconnect(agent_t *a)
{
    (void)a;
    /* handled from the tick: the pending step times out immediately */
    if (R.running && R.phase)
        R.deadline = 0;
}

void run_tick(void)
{
    if (!R.running || !R.phase || ls_now_us() < R.deadline)
        return;
    set_status(&R.paths[R.steps[R.cur].path], R.steps[R.cur].kind, ST_TIMEOUT);
    R.phase = 0;
    R.cur++;
    advance();
}

void run_cancel(void)
{
    if (!R.running)
        return;
    R.cancelled = 1;
    R.phase = 0;
    finish();
}

static void add_step(uint8_t kind, int path)
{
    R.steps[R.nstep].kind = kind;
    R.steps[R.nstep].path = path;
    R.nstep++;
}

int run_start(void)
{
    const agent_info *ptrs[MAX_AGENTS];
    int i, j, k, s;
    if (R.running)
        return -1;
    free_run();
    memset(&R, 0, sizeof(R));
    R.ag = calloc(MAX_AGENTS, sizeof(agent_info));
    R.segs = calloc(MAX_SEGS, sizeof(segment_t));
    if (!R.ag || !R.segs)
        return -2;
    for (i = 0; i < MAX_AGENTS; i++) {
        agent_t *a = &g_agents[i];
        if (a->used && a->authed && a->fd >= 0 && a->info.nifs) {
            R.ag[R.nag] = a->info;
            ptrs[R.nag] = &R.ag[R.nag];
            R.nag++;
        }
    }
    R.nseg = build_segments(ptrs, R.nag, R.segs, MAX_SEGS);
    for (s = 0; s < R.nseg; s++)
        R.npath += R.segs[s].nmem * (R.segs[s].nmem - 1);
    if (!R.npath) {
        free_run();
        return -3;
    }
    R.paths = calloc((size_t)R.npath, sizeof(path_t));
    R.steps = calloc((size_t)R.npath * 6, sizeof(step_t));
    if (!R.paths || !R.steps) {
        free_run();
        return -2;
    }
    R.npath = 0;
    for (s = 0; s < R.nseg; s++) {
        const segment_t *sg = &R.segs[s];
        for (i = 0; i < sg->nmem; i++) {
            for (j = 0; j < sg->nmem; j++) {
                path_t *p;
                const s_if *fa, *fb;
                if (i == j || sg->mem[i].agent == sg->mem[j].agent)
                    continue;
                p = &R.paths[R.npath];
                p->seg = s;
                p->a = sg->mem[i].agent;
                p->ai = sg->mem[i].ifi;
                p->b = sg->mem[j].agent;
                p->bi = sg->mem[j].ifi;
                p->ping_st = p->echo_st = p->pmtu_st = p->jumbo_st = ST_PENDING;
                p->tcp[0].st = p->tcp[1].st = ST_PENDING;
                fa = &R.ag[p->a].ifs[p->ai];
                fb = &R.ag[p->b].ifs[p->bi];
                p->jumbo_size = (uint16_t)(fa->mtu < fb->mtu ? fa->mtu : fb->mtu);
                k = R.npath++;
                add_step(SK_PING, k);
                add_step(SK_ECHO, k);
                add_step(SK_PMTU, k);
                if (p->jumbo_size > 1500 && p->jumbo_size <= 9216)
                    add_step(SK_JUMBO, k);
                add_step(SK_TCP1, k);
                if (g_conf.streams > 1)
                    add_step(SK_TCPN, k);
            }
        }
    }
    if (!R.npath) {
        free_run();
        return -3;
    }
    R.id = store_next_id();
    R.started = ls_wall_ms();
    R.streams = g_conf.streams;
    R.duration_ms = g_conf.duration_ms;
    R.running = 1;
    ls_random(&R.cmd_id, sizeof(R.cmd_id));
    ls_log(LOG_I, "run %u started: %d segments, %d paths, %d steps", R.id, R.nseg, R.npath, R.nstep);
    advance();
    return (int)R.id;
}

/* ---- verdicts and report ---- */

static void judge(path_t *p)
{
    const s_if *fa = &R.ag[p->a].ifs[p->ai];
    const s_if *fb = &R.ag[p->b].ifs[p->bi];
    const segment_t *sg = &R.segs[p->seg];
    int k, cpu = 0, ok = 0;
    p->expected = sg->manual_mbps;
    if (!p->expected && fa->speed && fb->speed)
        p->expected = fa->speed < fb->speed ? fa->speed : fb->speed;
    for (k = 0; k < 2; k++) {
        const tcp_r *t = &p->tcp[k];
        if (t->st == ST_ROUTE_MISMATCH || t->st == ST_COUNTER_MISMATCH)
            p->mismatch = 1;
        if (t->st == ST_OK) {
            ok = 1;
            if (t->bps > p->best)
                p->best = t->bps;
        }
        if ((t->st == ST_OK || t->st == ST_COUNTER_MISMATCH) && (t->lcpu >= 900 || t->pcpu >= 900))
            cpu = 1;
    }
    if (p->ping_st == ST_ROUTE_MISMATCH || p->echo_st == ST_ROUTE_MISMATCH)
        p->mismatch = 1;
    if (p->mismatch || !ok) {
        p->verdict = "red";
    } else if (!p->expected) {
        p->verdict = "none";
    } else {
        uint64_t pct = p->best / 10000u / p->expected; /* bps -> percent of Mbit/s */
        if (pct >= 85)
            p->verdict = "green";
        else
            p->verdict = cpu ? "purple" : pct >= 50 ? "yellow" : "red";
    }
}

static void w_if(jw *w, const s_if *f)
{
    char mac[20];
    jw_obj(w);
    jw_kstr(w, "name", f->name);
    ls_fmt(mac, sizeof(mac), "%h:%h:%h:%h:%h:%h", f->mac[0], f->mac[1], f->mac[2], f->mac[3], f->mac[4], f->mac[5]);
    jw_kstr(w, "mac", mac);
    jw_key(w, "ip");
    jw_ip(w, f->addr);
    jw_kint(w, "prefix", f->prefix);
    jw_kint(w, "vlan", f->vlan);
    jw_kuint(w, "speed", f->speed);
    jw_kuint(w, "mtu", f->mtu);
    jw_kbool(w, "up", (f->flags & IFF_LS_UP) != 0);
    jw_kbool(w, "carrier", (f->flags & IFF_LS_CARRIER) != 0);
    jw_kstr(w, "kind", f->kind);
    jw_kstr(w, "parent", f->parent);
    jw_end_obj(w);
}

void w_agent(jw *w, const agent_info *a, int online, uint64_t seen)
{
    int i;
    jw_obj(w);
    jw_kstr(w, "id", a->id);
    jw_kstr(w, "hostname", a->hostname);
    jw_kstr(w, "host_id", a->host_id);
    jw_kstr(w, "arch", a->arch);
    jw_kstr(w, "version", a->version);
    jw_kbool(w, "lite", 1);
    if (online >= 0) {
        jw_kbool(w, "online", online);
        jw_kuint(w, "seen", seen);
    }
    jw_key(w, "ifs");
    jw_arr(w);
    for (i = 0; i < a->nifs; i++)
        w_if(w, &a->ifs[i]);
    jw_end_arr(w);
    jw_end_obj(w);
}

void w_segments(jw *w, const segment_t *segs, int n, const agent_info *const *ag)
{
    int s, m;
    jw_arr(w);
    for (s = 0; s < n; s++) {
        const segment_t *sg = &segs[s];
        char name[48];
        jw_obj(w);
        seg_name(sg, name, sizeof(name));
        jw_kstr(w, "id", name);
        ls_fmt(name, sizeof(name), "%I/%d", sg->net, sg->prefix);
        jw_kstr(w, "cidr", name);
        jw_kint(w, "vlan", sg->vlan);
        jw_kuint(w, "expected_mbps", sg->manual_mbps);
        jw_kbool(w, "manual", sg->manual_mbps != 0);
        jw_key(w, "members");
        jw_arr(w);
        for (m = 0; m < sg->nmem; m++) {
            const agent_info *a = ag[sg->mem[m].agent];
            const s_if *f = &a->ifs[sg->mem[m].ifi];
            jw_obj(w);
            jw_kstr(w, "agent", a->id);
            jw_kstr(w, "if", f->name);
            jw_key(w, "ip");
            jw_ip(w, f->addr);
            jw_kuint(w, "speed", f->speed);
            jw_kuint(w, "mtu", f->mtu);
            jw_kstr(w, "kind", f->kind);
            jw_end_obj(w);
        }
        jw_end_arr(w);
        jw_end_obj(w);
    }
    jw_end_arr(w);
}

static void w_tcp(jw *w, const char *key, const tcp_r *t, uint32_t streams)
{
    jw_key(w, key);
    if (t->st == ST_PENDING) {
        jw_null(w);
        return;
    }
    jw_obj(w);
    jw_kstr(w, "status", st_name(t->st));
    jw_kuint(w, "streams", streams);
    jw_kuint(w, "bps", t->bps);
    jw_kuint(w, "cpu_local", t->lcpu);
    jw_kuint(w, "cpu_peer", t->pcpu);
    jw_kbool(w, "path_ok", t->path_ok);
    jw_end_obj(w);
}

static void problem(jw *w, const char *kind, const path_t *p, const char *detail)
{
    jw_obj(w);
    jw_kstr(w, "kind", kind);
    jw_kint(w, "seg", p->seg);
    jw_kstr(w, "src", R.ag[p->a].id);
    jw_kstr(w, "src_if", R.ag[p->a].ifs[p->ai].name);
    jw_kstr(w, "dst", R.ag[p->b].id);
    jw_kstr(w, "dst_if", R.ag[p->b].ifs[p->bi].name);
    jw_kstr(w, "detail", detail);
    jw_end_obj(w);
}

static const path_t *reverse_of(const path_t *p)
{
    int i;
    for (i = 0; i < R.npath; i++) {
        const path_t *q = &R.paths[i];
        if (q->seg == p->seg && q->a == p->b && q->ai == p->bi && q->b == p->a && q->bi == p->ai)
            return q;
    }
    return NULL;
}

static int reachable_by_others(int agent, int ifi, int except)
{
    int i;
    for (i = 0; i < R.npath; i++) {
        const path_t *q = &R.paths[i];
        if (((q->a == agent && q->ai == ifi && q->b != except) || (q->b == agent && q->bi == ifi && q->a != except)) &&
            q->ping_st == ST_OK)
            return 1;
    }
    return 0;
}

static void problems(jw *w)
{
    int i;
    char d[160];
    jw_arr(w);
    for (i = 0; i < R.npath; i++) {
        const path_t *p = &R.paths[i];
        const path_t *rv = reverse_of(p);
        const s_if *fa = &R.ag[p->a].ifs[p->ai];
        const s_if *fb = &R.ag[p->b].ifs[p->bi];
        int ping_ok = p->ping_st == ST_OK;
        if (p->mismatch) {
            if (p->route_dev[0])
                ls_fmt(d, sizeof(d), "route to %I leaves via %s, not %s", fb->addr, p->route_dev, fa->name);
            else
                ls_fmt(d, sizeof(d), "interface counters of %s did not grow with the test traffic", fa->name);
            problem(w, "path_mismatch", p, d);
            continue;
        }
        if (!ping_ok && p->echo_st != ST_OK && p->ping_st != ST_OFFLINE) {
            int mv_a = !strcmp(fa->kind, "macvlan"), mv_b = !strcmp(fb->kind, "macvlan");
            if ((mv_a != mv_b) && rv && rv->ping_st != ST_OK && reachable_by_others(p->a, p->ai, p->b) &&
                reachable_by_others(p->b, p->bi, p->a)) {
                /* report once per pair, from the host (non-macvlan) side */
                if (mv_b) {
                    ls_fmt(d, sizeof(d),
                           "%s (%s) cannot reach macvlan child %s (%I); add a macvlan sibling on the host parent "
                           "interface and move the host address there",
                           R.ag[p->a].id, fa->name, R.ag[p->b].id, fb->addr);
                    problem(w, "macvlan", p, d);
                }
                continue;
            }
            ls_fmt(d, sizeof(d), "no ICMP or TCP reply from %I", fb->addr);
            problem(w, "unreachable", p, d);
            continue;
        }
        if (ping_ok && p->echo_st != ST_OK && p->echo_st != ST_OFFLINE) {
            ls_fmt(d, sizeof(d), "ICMP to %I works but TCP to port %u fails (%s): transparent proxy or filter",
                   fb->addr, R.ag[p->b].data_port, st_name(p->echo_st));
            problem(w, "tcp_intercepted", p, d);
        }
        if (ping_ok && p->pmtu_st != ST_OK && p->pmtu_st != ST_OFFLINE && p->pmtu_st != ST_PENDING) {
            ls_fmt(d, sizeof(d), "1500-byte packets with DF do not pass to %I (%s)", fb->addr, st_name(p->pmtu_st));
            problem(w, "mtu", p, d);
        } else if (ping_ok && p->jumbo_st != ST_OK && p->jumbo_st != ST_PENDING && p->jumbo_st != ST_OFFLINE) {
            ls_fmt(d, sizeof(d), "jumbo frames of %u bytes do not pass to %I", p->jumbo_size, fb->addr);
            problem(w, "mtu", p, d);
        }
        if (ping_ok && p->recv < p->sent && p->sent && (uint32_t)(p->sent - p->recv) * 1000u / p->sent > 5) {
            ls_fmt(d, sizeof(d), "%u of %u ICMP packets lost", p->sent - p->recv, p->sent);
            problem(w, "loss", p, d);
        }
        if (!strcmp(p->verdict, "purple")) {
            ls_fmt(d, sizeof(d), "%U Mbit/s of %u expected, limited by agent CPU", p->best / 1000000u, p->expected);
            problem(w, "cpu_bound", p, d);
        } else if (p->best && (!strcmp(p->verdict, "yellow") || !strcmp(p->verdict, "red"))) {
            ls_fmt(d, sizeof(d), "%U Mbit/s of %u expected", p->best / 1000000u, p->expected);
            problem(w, "slow", p, d);
        }
    }
    jw_end_arr(w);
}

static char *report(size_t *len)
{
    const agent_info *ptrs[MAX_AGENTS];
    jw w;
    int i;
    for (i = 0; i < R.nag; i++)
        ptrs[i] = &R.ag[i];
    for (i = 0; i < R.npath; i++)
        judge(&R.paths[i]);
    jw_init(&w);
    jw_obj(&w);
    jw_kuint(&w, "id", R.id);
    jw_kuint(&w, "started", R.started);
    jw_kuint(&w, "finished", ls_wall_ms());
    jw_kstr(&w, "status", R.cancelled ? "cancelled" : "done");
    jw_kuint(&w, "duration_ms", R.duration_ms);
    jw_kuint(&w, "streams", R.streams);
    jw_key(&w, "agents");
    jw_arr(&w);
    for (i = 0; i < R.nag; i++)
        w_agent(&w, &R.ag[i], -1, 0);
    jw_end_arr(&w);
    jw_key(&w, "segments");
    w_segments(&w, R.segs, R.nseg, ptrs);
    jw_key(&w, "paths");
    jw_arr(&w);
    for (i = 0; i < R.npath; i++) {
        const path_t *p = &R.paths[i];
        const s_if *fa = &R.ag[p->a].ifs[p->ai];
        const s_if *fb = &R.ag[p->b].ifs[p->bi];
        jw_obj(&w);
        jw_kint(&w, "seg", p->seg);
        jw_kstr(&w, "src", R.ag[p->a].id);
        jw_kstr(&w, "src_if", fa->name);
        jw_key(&w, "src_ip");
        jw_ip(&w, fa->addr);
        jw_kstr(&w, "dst", R.ag[p->b].id);
        jw_kstr(&w, "dst_if", fb->name);
        jw_key(&w, "dst_ip");
        jw_ip(&w, fb->addr);
        jw_key(&w, "ping");
        jw_obj(&w);
        jw_kstr(&w, "status", st_name(p->ping_st));
        jw_kuint(&w, "sent", p->sent);
        jw_kuint(&w, "recv", p->recv);
        jw_kuint(&w, "rtt_min_us", p->rmin);
        jw_kuint(&w, "rtt_avg_us", p->ravg);
        jw_kuint(&w, "rtt_max_us", p->rmax);
        jw_kbool(&w, "rtt_high", p->recv && p->ravg > g_conf.rtt_warn_ms * 1000u);
        jw_end_obj(&w);
        jw_key(&w, "echo");
        jw_obj(&w);
        jw_kstr(&w, "status", st_name(p->echo_st));
        jw_kuint(&w, "rtt_us", p->echo_rtt);
        jw_end_obj(&w);
        jw_key(&w, "mtu");
        jw_obj(&w);
        jw_kstr(&w, "status", st_name(p->pmtu_st));
        jw_kuint(&w, "size", 1500);
        if (p->jumbo_st != ST_PENDING) {
            jw_kstr(&w, "jumbo_status", st_name(p->jumbo_st));
            jw_kuint(&w, "jumbo_size", p->jumbo_size);
        }
        jw_end_obj(&w);
        w_tcp(&w, "tcp1", &p->tcp[0], 1);
        w_tcp(&w, "tcpn", &p->tcp[1], R.streams);
        if (p->route_dev[0])
            jw_kstr(&w, "route_dev", p->route_dev);
        jw_kuint(&w, "expected_mbps", p->expected);
        jw_kuint(&w, "best_bps", p->best);
        jw_kbool(&w, "mismatch", p->mismatch);
        jw_kstr(&w, "verdict", p->verdict);
        jw_end_obj(&w);
    }
    jw_end_arr(&w);
    jw_key(&w, "problems");
    problems(&w);
    jw_end_obj(&w);
    return jw_take(&w, len);
}

static void finish(void)
{
    size_t len = 0;
    char *json;
    char ev[64];
    if (!R.running)
        return;
    json = report(&len);
    R.running = 0;
    R.phase = 0;
    ls_log(LOG_I, "run %u finished (%d/%d steps)", R.id, R.cur, R.nstep);
    if (json) {
        webhook_post(json, len);
        store_add(json, len);
    }
    ls_fmt(ev, sizeof(ev), "{\"run\":%u}", R.id);
    sse_broadcast("done", ev, strlen(ev));
    free_run();
}

void run_state_json(void *wp)
{
    jw *w = wp;
    uint64_t eta = 0;
    int i;
    jw_kbool(w, "running", R.running);
    if (!R.running)
        return;
    for (i = R.cur; i < R.nstep; i++)
        eta += step_cost_ms(&R.steps[i]);
    jw_key(w, "run");
    jw_obj(w);
    jw_kuint(w, "id", R.id);
    jw_kuint(w, "started", R.started);
    jw_kint(w, "done", R.cur);
    jw_kint(w, "total", R.nstep);
    jw_kuint(w, "eta_s", eta / 1000);
    jw_end_obj(w);
}
