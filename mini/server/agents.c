/* Agent control connections (port 47701): authentication, inventory, keepalive. */
#include "server.h"
#include "../common/sha256.h"
#include "../common/tlv.h"
#include "../common/util.h"

#include <errno.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

#define AG_RBUF 70000

agent_t g_agents[MAX_AGENTS];

agent_t *agent_by_id(const char *id)
{
    int i;
    for (i = 0; i < MAX_AGENTS; i++)
        if (g_agents[i].used && g_agents[i].authed && !strcmp(g_agents[i].info.id, id))
            return &g_agents[i];
    return NULL;
}

int agent_send(agent_t *a, const uint8_t *frame, size_t len)
{
    if (a->fd < 0)
        return -1;
    if (ls_write_all(a->fd, frame, len, ls_now_us() + 2000000u) != LSR_OK)
        return -1;
    a->last_tx = ls_now_us();
    return 0;
}

static int send_w(agent_t *a, ls_wr *w)
{
    size_t n = ls_frame_end(w);
    return n ? agent_send(a, w->p + w->frame, n) : -1;
}

static void drop(agent_t *a, const char *why)
{
    if (a->fd >= 0) {
        close(a->fd);
        a->fd = -1;
    }
    free(a->rbuf);
    a->rbuf = NULL;
    a->rlen = 0;
    if (a->authed) {
        ls_log(LOG_I, "agent %s offline: %s", a->info.id, why);
        a->seen_ms = ls_wall_ms();
        run_on_disconnect(a);
    } else {
        memset(a, 0, sizeof(*a));
        a->fd = -1;
    }
}

void agents_accept(int lfd)
{
    struct sockaddr_in sa;
    socklen_t sl = sizeof(sa);
    int fd = accept(lfd, (struct sockaddr *)&sa, &sl), i, one = 1;
    agent_t *a = NULL;
    if (fd < 0)
        return;
    for (i = 0; i < MAX_AGENTS; i++)
        if (!g_agents[i].used) {
            a = &g_agents[i];
            break;
        }
    if (!a) {
        /* reuse the oldest offline slot */
        for (i = 0; i < MAX_AGENTS; i++)
            if (g_agents[i].fd < 0 && (!a || g_agents[i].seen_ms < a->seen_ms))
                a = &g_agents[i];
        if (!a) {
            close(fd);
            return;
        }
    }
    memset(a, 0, sizeof(*a));
    a->rbuf = malloc(AG_RBUF);
    if (!a->rbuf) {
        close(fd);
        a->fd = -1;
        return;
    }
    a->used = 1;
    a->fd = fd;
    a->peer = sa.sin_addr.s_addr;
    a->last_rx = a->last_tx = ls_now_us();
    setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof(one));
    ls_set_nonblock(fd);
}

int agents_pollfds(struct pollfd *p, int max)
{
    int i, n = 0;
    for (i = 0; i < MAX_AGENTS && n < max; i++) {
        if (!g_agents[i].used || g_agents[i].fd < 0)
            continue;
        p[n].fd = g_agents[i].fd;
        p[n].events = POLLIN;
        p[n].revents = 0;
        n++;
    }
    return n;
}

static void labelled_mac(const char *label, const uint8_t *a, const uint8_t *b, uint8_t *out)
{
    ls_hmac h;
    ls_hmac_init(&h, g_conf.token, strlen(g_conf.token));
    ls_hmac_update(&h, label, strlen(label));
    ls_hmac_update(&h, a, LSTP_NONCE_LEN);
    ls_hmac_update(&h, b, LSTP_NONCE_LEN);
    ls_hmac_final(&h, out);
}

static void parse_inventory(agent_t *a, const uint8_t *pl, size_t plen)
{
    ls_rd r;
    uint8_t t;
    const uint8_t *v;
    size_t vl;
    int n = 0;
    ls_rd_init(&r, pl, plen);
    while (ls_next(&r, &t, &v, &vl) == 1 && n < MAX_AG_IFS) {
        s_if *f = &a->info.ifs[n];
        const uint8_t *x;
        size_t xl;
        uint8_t u8 = 0;
        if (t != T_IFACE)
            continue;
        memset(f, 0, sizeof(*f));
        if (!ls_get_str(v, vl, T_IF_NAME, f->name, sizeof(f->name)) || !ls_get(v, vl, T_IF_ADDR4, &x, &xl) ||
            xl != 4)
            continue;
        memcpy(&f->addr, x, 4);
        if (ls_get(v, vl, T_IF_MAC, &x, &xl) && xl == 6)
            memcpy(f->mac, x, 6);
        if (ls_get_u8(v, vl, T_IF_PREFIX, &u8) && u8 <= 32)
            f->prefix = u8;
        ls_get_u16(v, vl, T_IF_VLAN, &f->vlan);
        ls_get_u32(v, vl, T_IF_SPEED, &f->speed);
        ls_get_u32(v, vl, T_IF_MTU, &f->mtu);
        ls_get_u32(v, vl, T_IF_FLAGS, &f->flags);
        ls_get_str(v, vl, T_IF_KIND, f->kind, sizeof(f->kind));
        ls_get_str(v, vl, T_IF_PARENT, f->parent, sizeof(f->parent));
        n++;
    }
    a->info.nifs = n;
    ls_log(LOG_D, "agent %s inventory: %d interfaces", a->info.id, n);
}

static int on_frame(agent_t *a, uint8_t type, const uint8_t *pl, size_t plen)
{
    uint8_t buf[256];
    ls_wr w;
    if (!a->authed) {
        if (type == LC_HELLO && !a->info.id[0]) {
            const uint8_t *n1;
            size_t nl;
            if (!ls_get_str(pl, plen, T_AGENT_ID, a->info.id, sizeof(a->info.id)) || !a->info.id[0] ||
                !ls_get(pl, plen, T_NONCE, &n1, &nl) || nl != LSTP_NONCE_LEN)
                return -1;
            memcpy(a->n1, n1, LSTP_NONCE_LEN);
            ls_get_str(pl, plen, T_VERSION, a->info.version, sizeof(a->info.version));
            ls_get_str(pl, plen, T_HOSTNAME, a->info.hostname, sizeof(a->info.hostname));
            ls_get_str(pl, plen, T_HOST_ID, a->info.host_id, sizeof(a->info.host_id));
            ls_get_str(pl, plen, T_ARCH, a->info.arch, sizeof(a->info.arch));
            ls_get_u32(pl, plen, T_CAPS, &a->info.caps);
            a->info.data_port = LSTP_DATA_PORT;
            ls_get_u16(pl, plen, T_DST_PORT, &a->info.data_port);
            ls_random(a->n2, sizeof(a->n2));
            ls_wr_init(&w, buf, sizeof(buf));
            ls_frame_begin(&w, LC_CHALLENGE);
            ls_put(&w, T_NONCE, a->n2, sizeof(a->n2));
            return send_w(a, &w);
        }
        if (type == LC_AUTH && a->info.id[0]) {
            uint8_t mac[LS_SHA256_LEN];
            const uint8_t *m;
            size_t ml;
            agent_t *old;
            int i;
            labelled_mac("agent", a->n1, a->n2, mac);
            if (!ls_get(pl, plen, T_MAC, &m, &ml) || ml != LS_SHA256_LEN || !ls_ct_equal(mac, m, ml)) {
                ls_log(LOG_W, "agent %s: bad token", a->info.id);
                return -1;
            }
            /* a reconnecting agent replaces its previous slot */
            for (i = 0; i < MAX_AGENTS; i++) {
                old = &g_agents[i];
                if (old != a && old->used && old->authed && !strcmp(old->info.id, a->info.id)) {
                    if (old->fd >= 0)
                        drop(old, "replaced by a new connection");
                    memset(old, 0, sizeof(*old));
                    old->fd = -1;
                }
            }
            a->authed = 1;
            labelled_mac("server", a->n2, a->n1, mac);
            ls_wr_init(&w, buf, sizeof(buf));
            ls_frame_begin(&w, LC_WELCOME);
            ls_put(&w, T_SERVER_MAC, mac, sizeof(mac));
            ls_log(LOG_I, "agent %s online (%s %s)", a->info.id, a->info.arch, a->info.version);
            return send_w(a, &w);
        }
        return -1;
    }
    switch (type) {
    case LC_INVENTORY:
        parse_inventory(a, pl, plen);
        return 0;
    case LC_PING:
        ls_wr_init(&w, buf, sizeof(buf));
        ls_frame_begin(&w, LC_PONG);
        return send_w(a, &w);
    case LC_PONG:
        return 0;
    case LC_GRANT_ACK:
    case LC_CMD_RESULT:
        run_on_frame(a, type, pl, plen);
        return 0;
    default:
        return 0;
    }
}

void agents_handle(int fd, short revents)
{
    agent_t *a = NULL;
    int i;
    ssize_t n;
    for (i = 0; i < MAX_AGENTS; i++)
        if (g_agents[i].used && g_agents[i].fd == fd)
            a = &g_agents[i];
    if (!a || !(revents & (POLLIN | POLLHUP | POLLERR)))
        return;
    n = read(fd, a->rbuf + a->rlen, AG_RBUF - a->rlen);
    if (n == 0 || (n < 0 && errno != EAGAIN && errno != EINTR)) {
        drop(a, "connection closed");
        return;
    }
    if (n < 0)
        return;
    a->rlen += (size_t)n;
    a->last_rx = ls_now_us();
    a->seen_ms = ls_wall_ms();
    for (;;) {
        uint8_t type;
        const uint8_t *pl;
        size_t plen;
        long fl = ls_frame_parse(a->rbuf, a->rlen, &type, &pl, &plen);
        if (fl < 0) {
            drop(a, "protocol error");
            return;
        }
        if (fl == 0)
            break;
        if (on_frame(a, type, pl, plen)) {
            drop(a, "handshake failed");
            return;
        }
        if (a->fd < 0)
            return;
        memmove(a->rbuf, a->rbuf + fl, a->rlen - (size_t)fl);
        a->rlen -= (size_t)fl;
    }
}

void agents_tick(void)
{
    uint64_t now = ls_now_us();
    int i;
    for (i = 0; i < MAX_AGENTS; i++) {
        agent_t *a = &g_agents[i];
        if (!a->used || a->fd < 0)
            continue;
        if (!a->authed && now - a->last_rx > 10000000u) {
            drop(a, "handshake timeout");
            continue;
        }
        if (now - a->last_rx > 90000000u) {
            drop(a, "keepalive timeout");
            continue;
        }
        if (a->authed && now - a->last_tx > 30000000u) {
            uint8_t buf[16];
            ls_wr w;
            ls_wr_init(&w, buf, sizeof(buf));
            ls_frame_begin(&w, LC_PING);
            if (send_w(a, &w))
                drop(a, "write failed");
        }
    }
}
