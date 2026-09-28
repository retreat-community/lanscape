/* LSTP/1 data plane: responder (port 47700) and initiator. */
#include "agent.h"
#include "../common/netif.h"
#include "../common/sha256.h"
#include "../common/tlv.h"
#include "../common/util.h"

#include <errno.h>
#include <linux/sockios.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <poll.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/socket.h>
#include <unistd.h>

#define MAX_GRANTS 16
#define MAX_STREAMS 16
#define HS_TIMEOUT_US 3000000u
#define FBUF 2048

typedef struct {
    uint32_t run_id;
    uint8_t token[LSTP_TOKEN_LEN];
    uint64_t expires;
} grant_t;

static grant_t grants[MAX_GRANTS];
static uint8_t iobuf[65536];

void dp_grant(uint32_t run_id, const uint8_t *token, uint32_t ttl_s)
{
    uint64_t now = ls_now_us();
    int i, slot = 0;
    if (ttl_s == 0 || ttl_s > LSTP_TOKEN_TTL_S)
        ttl_s = LSTP_TOKEN_TTL_S;
    for (i = 0; i < MAX_GRANTS; i++) {
        if (grants[i].run_id == run_id || grants[i].expires < now) {
            slot = i;
            break;
        }
        if (grants[i].expires < grants[slot].expires)
            slot = i;
    }
    grants[slot].run_id = run_id;
    memcpy(grants[slot].token, token, LSTP_TOKEN_LEN);
    grants[slot].expires = now + (uint64_t)ttl_s * 1000000u;
}

static const grant_t *find_grant(uint32_t run_id)
{
    uint64_t now = ls_now_us();
    int i;
    for (i = 0; i < MAX_GRANTS; i++)
        if (grants[i].run_id == run_id && grants[i].expires > now)
            return &grants[i];
    return NULL;
}

static void auth_mac(const uint8_t *token, const uint8_t *n1, const uint8_t *n2, uint8_t *out)
{
    ls_hmac h;
    ls_hmac_init(&h, token, LSTP_TOKEN_LEN);
    ls_hmac_update(&h, n1, LSTP_NONCE_LEN);
    ls_hmac_update(&h, n2, LSTP_NONCE_LEN);
    ls_hmac_final(&h, out);
}

static int send_frame(int fd, ls_wr *w, uint64_t deadline)
{
    size_t n = ls_frame_end(w);
    if (!n)
        return LSR_ERR;
    return ls_write_all(fd, w->p + w->frame, n, deadline);
}

static void send_error(int fd, uint8_t code)
{
    uint8_t b[32];
    ls_wr w;
    ls_wr_init(&w, b, sizeof(b));
    ls_frame_begin(&w, LS_ERROR);
    ls_put_u8(&w, T_ERR_CODE, code);
    send_frame(fd, &w, ls_now_us() + 500000u);
}

static void sock_setup(int fd)
{
    int one = 1;
    setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof(one));
    ls_set_nonblock(fd);
}

/* ---- shared transfer loops (no allocation) ---- */

static uint64_t send_loop(int *fds, int n, uint64_t end)
{
    struct pollfd p[MAX_STREAMS];
    uint64_t total = 0;
    int i;
    for (;;) {
        uint64_t now = ls_now_us();
        if (now >= end)
            break;
        for (i = 0; i < n; i++) {
            p[i].fd = fds[i];
            p[i].events = POLLOUT;
            p[i].revents = 0;
        }
        if (poll(p, (nfds_t)n, (int)((end - now) / 1000) + 1) <= 0)
            continue;
        for (i = 0; i < n; i++) {
            ssize_t w;
            if (fds[i] < 0)
                continue;
            if (p[i].revents & (POLLERR | POLLHUP)) {
                close(fds[i]);
                fds[i] = -1;
                continue;
            }
            if (!(p[i].revents & POLLOUT))
                continue;
            w = send(fds[i], iobuf, sizeof(iobuf), MSG_NOSIGNAL);
            if (w > 0)
                total += (uint64_t)w;
            else if (w < 0 && errno != EAGAIN && errno != EINTR) {
                close(fds[i]);
                fds[i] = -1;
            }
        }
    }
    for (i = 0; i < n; i++)
        if (fds[i] >= 0)
            shutdown(fds[i], SHUT_WR);
    return total;
}

/* Waits until the kernel has transmitted everything queued on the sockets,
 * so that interface counters read afterwards include the whole payload. */
static void drain_wait(const int *fds, int n, uint64_t deadline)
{
    while (ls_now_us() < deadline) {
        int i, pending = 0;
        for (i = 0; i < n; i++) {
            int q = 0;
            if (fds[i] >= 0 && ioctl(fds[i], SIOCOUTQ, &q) == 0 && q > 0)
                pending = 1;
        }
        if (!pending)
            return;
        usleep(20000);
    }
}

typedef struct {
    uint64_t total;
    uint64_t counted;
    uint64_t window_us;
} rx_stat;

static void recv_loop(int *fds, int n, uint64_t deadline, uint32_t warmup_ms, rx_stat *st)
{
    struct pollfd p[MAX_STREAMS];
    uint64_t first = 0, ws = 0, last = 0;
    int open_n = n, i;
    memset(st, 0, sizeof(*st));
    while (open_n > 0) {
        uint64_t now = ls_now_us();
        int k = 0;
        if (now >= deadline)
            break;
        for (i = 0; i < n; i++) {
            if (fds[i] < 0)
                continue;
            p[k].fd = fds[i];
            p[k].events = POLLIN;
            p[k].revents = 0;
            k++;
        }
        if (poll(p, (nfds_t)k, (int)((deadline - now) / 1000) + 1) <= 0)
            continue;
        for (i = 0; i < k; i++) {
            ssize_t r;
            int j;
            if (!(p[i].revents & (POLLIN | POLLHUP | POLLERR)))
                continue;
            r = recv(p[i].fd, iobuf, sizeof(iobuf), 0);
            if (r < 0 && (errno == EAGAIN || errno == EINTR))
                continue;
            if (r <= 0) {
                for (j = 0; j < n; j++)
                    if (fds[j] == p[i].fd)
                        fds[j] = -1;
                close(p[i].fd);
                open_n--;
                continue;
            }
            now = ls_now_us();
            if (!first) {
                first = now;
                ws = first + (uint64_t)warmup_ms * 1000u;
            }
            st->total += (uint64_t)r;
            if (now >= ws) {
                st->counted += (uint64_t)r;
                last = now;
            }
        }
    }
    if (last > ws)
        st->window_us = last - ws;
}

static unsigned pct90_ok(uint64_t iface, uint64_t payload)
{
    return payload == 0 || iface >= payload / 10 * 9;
}

/* ---- responder ---- */

static int accept_data(int lfd, uint32_t run_id, uint32_t session, const uint8_t *token,
                       int *out, int want, uint64_t deadline)
{
    static uint8_t fb[FBUF];
    int got = 0;
    while (got < want) {
        struct pollfd p;
        uint64_t now = ls_now_us();
        int fd;
        uint8_t type, n2[LSTP_NONCE_LEN], mac[LS_SHA256_LEN];
        const uint8_t *pl, *n1, *am;
        size_t plen, n1l, aml;
        uint32_t rid = 0, sess = 0;
        uint8_t role = 0xff;
        ls_wr w;
        if (now >= deadline)
            return got;
        p.fd = lfd;
        p.events = POLLIN;
        if (poll(&p, 1, (int)((deadline - now) / 1000) + 1) <= 0)
            continue;
        fd = accept(lfd, NULL, NULL);
        if (fd < 0)
            continue;
        sock_setup(fd);
        now = ls_now_us();
        if (ls_read_frame(fd, fb, sizeof(fb), now + HS_TIMEOUT_US, &type, &pl, &plen) != LSR_OK ||
            type != LS_HELLO || !ls_get_u32(pl, plen, T_RUN_ID, &rid) ||
            !ls_get_u32(pl, plen, T_SESSION, &sess) || !ls_get_u8(pl, plen, T_ROLE, &role) ||
            !ls_get(pl, plen, T_NONCE, &n1, &n1l) || n1l != LSTP_NONCE_LEN || rid != run_id ||
            sess != session || role != ROLE_DATA) {
            close(fd);
            continue;
        }
        {
            uint8_t n1c[LSTP_NONCE_LEN];
            memcpy(n1c, n1, LSTP_NONCE_LEN);
            ls_random(n2, sizeof(n2));
            ls_wr_init(&w, fb, sizeof(fb));
            ls_frame_begin(&w, LS_CHALLENGE);
            ls_put(&w, T_NONCE, n2, sizeof(n2));
            if (send_frame(fd, &w, now + HS_TIMEOUT_US) != LSR_OK ||
                ls_read_frame(fd, fb, sizeof(fb), now + HS_TIMEOUT_US, &type, &pl, &plen) != LSR_OK ||
                type != LS_AUTH || !ls_get(pl, plen, T_MAC, &am, &aml) || aml != LS_SHA256_LEN) {
                close(fd);
                continue;
            }
            auth_mac(token, n1c, n2, mac);
            if (!ls_ct_equal(mac, am, LS_SHA256_LEN)) {
                close(fd);
                continue;
            }
        }
        ls_wr_init(&w, fb, sizeof(fb));
        ls_frame_begin(&w, LS_READY);
        ls_put_u32(&w, T_SESSION, session);
        if (send_frame(fd, &w, now + HS_TIMEOUT_US) != LSR_OK) {
            close(fd);
            continue;
        }
        out[got++] = fd;
    }
    return got;
}

static void local_dev(int fd, char *dev, size_t cap)
{
    struct sockaddr_in sa;
    socklen_t sl = sizeof(sa);
    dev[0] = 0;
    if (getsockname(fd, (struct sockaddr *)&sa, &sl) == 0)
        ls_dev_by_addr(sa.sin_addr.s_addr, dev, cap);
}

void dp_respond(int cfd, int lfd)
{
    static uint8_t fb[FBUF];
    uint8_t type, kind = 0, dir = 0, streams = 1, role = 0xff;
    const uint8_t *pl, *nv, *am;
    size_t plen, nl, aml;
    uint32_t run_id = 0, duration = 0, session, warmup = LSTP_WARMUP_MS;
    uint8_t n1[LSTP_NONCE_LEN], n2[LSTP_NONCE_LEN], mac[LS_SHA256_LEN];
    const grant_t *g;
    uint64_t now = ls_now_us();
    int data[MAX_STREAMS];
    int nd = 0, i;
    ls_wr w;

    sock_setup(cfd);
    if (ls_read_frame(cfd, fb, sizeof(fb), now + HS_TIMEOUT_US, &type, &pl, &plen) != LSR_OK ||
        type != LS_HELLO || !ls_get_u32(pl, plen, T_RUN_ID, &run_id) ||
        !ls_get(pl, plen, T_NONCE, &nv, &nl) || nl != LSTP_NONCE_LEN ||
        !ls_get_u8(pl, plen, T_ROLE, &role) || role != ROLE_CTRL || !(g = find_grant(run_id))) {
        /* no valid run: close immediately without a reply */
        close(cfd);
        return;
    }
    memcpy(n1, nv, sizeof(n1));
    ls_get_u8(pl, plen, T_TEST_KIND, &kind);
    ls_get_u8(pl, plen, T_DIRECTION, &dir);
    ls_get_u8(pl, plen, T_STREAMS, &streams);
    ls_get_u32(pl, plen, T_DURATION_MS, &duration);
    ls_get_u32(pl, plen, T_WARMUP_MS, &warmup);
    ls_random(n2, sizeof(n2));
    ls_wr_init(&w, fb, sizeof(fb));
    ls_frame_begin(&w, LS_CHALLENGE);
    ls_put(&w, T_NONCE, n2, sizeof(n2));
    if (send_frame(cfd, &w, now + HS_TIMEOUT_US) != LSR_OK ||
        ls_read_frame(cfd, fb, sizeof(fb), now + HS_TIMEOUT_US, &type, &pl, &plen) != LSR_OK ||
        type != LS_AUTH || !ls_get(pl, plen, T_MAC, &am, &aml) || aml != LS_SHA256_LEN) {
        close(cfd);
        return;
    }
    auth_mac(g->token, n1, n2, mac);
    if (!ls_ct_equal(mac, am, LS_SHA256_LEN)) {
        ls_log(LOG_W, "dataplane auth failed run=%u", run_id);
        close(cfd);
        return;
    }
    if (kind != TK_ECHO && kind != TK_TCP_THROUGHPUT) {
        send_error(cfd, E_UNSUPPORTED);
        close(cfd);
        return;
    }
    if (duration > g_conf.max_duration_ms || streams == 0 || streams > g_conf.max_streams ||
        streams > MAX_STREAMS || dir > DIR_REVERSE || warmup > 5000) {
        send_error(cfd, E_LIMIT);
        close(cfd);
        return;
    }
    ls_random(&session, sizeof(session));
    ls_wr_init(&w, fb, sizeof(fb));
    ls_frame_begin(&w, LS_READY);
    ls_put_u32(&w, T_SESSION, session);
    if (send_frame(cfd, &w, now + HS_TIMEOUT_US) != LSR_OK) {
        close(cfd);
        return;
    }

    if (kind == TK_ECHO) {
        for (;;) {
            int rc = ls_read_frame(cfd, fb, sizeof(fb), ls_now_us() + 10000000u, &type, &pl, &plen);
            if (rc != LSR_OK || type != LS_ECHO_REQ)
                break;
            /* echo the payload back unchanged; header differs only in type */
            fb[3] = LS_ECHO_REP;
            if (ls_write_all(cfd, fb, LSTP_HDR + plen, ls_now_us() + 1000000u) != LSR_OK)
                break;
        }
        close(cfd);
        return;
    }

    nd = accept_data(lfd, run_id, session, g->token, data, streams, ls_now_us() + 5000000u);
    if (nd == streams &&
        ls_read_frame(cfd, fb, sizeof(fb), ls_now_us() + 5000000u, &type, &pl, &plen) == LSR_OK &&
        type == LS_START) {
        char dev[16];
        uint64_t rx0 = 0, tx0 = 0, rx1 = 0, tx1 = 0;
        ls_cpu c0, c1;
        local_dev(cfd, dev, sizeof(dev));
        ls_if_counters(dev, &rx0, &tx0);
        ls_cpu_read(&c0);
        ls_wr_init(&w, fb, sizeof(fb));
        if (dir == DIR_FORWARD) {
            rx_stat st;
            recv_loop(data, nd, ls_now_us() + (uint64_t)(duration + warmup) * 1000u + 3000000u,
                      warmup, &st);
            ls_cpu_read(&c1);
            ls_if_counters(dev, &rx1, &tx1);
            ls_frame_begin(&w, LS_RESULT);
            ls_put_u64(&w, T_BYTES, st.counted);
            ls_put_u64(&w, T_WINDOW_US, st.window_us);
            ls_put_u64(&w, T_PKTS_RECV, st.total);
            ls_put_u16(&w, T_CPU, (uint16_t)ls_cpu_permille(&c0, &c1));
            ls_put_u64(&w, T_IF_RX, rx1 - rx0);
            ls_put_u8(&w, T_PATH_OK, (uint8_t)pct90_ok(rx1 - rx0, st.total));
        } else {
            uint64_t sent = send_loop(data, nd, ls_now_us() + (uint64_t)(duration + warmup) * 1000u);
            ls_cpu_read(&c1);
            drain_wait(data, nd, ls_now_us() + 5000000u);
            ls_if_counters(dev, &rx1, &tx1);
            ls_frame_begin(&w, LS_SENDER_STAT);
            ls_put_u64(&w, T_BYTES, sent);
            ls_put_u16(&w, T_CPU, (uint16_t)ls_cpu_permille(&c0, &c1));
            ls_put_u64(&w, T_IF_TX, tx1 - tx0);
            ls_put_u8(&w, T_PATH_OK, (uint8_t)pct90_ok(tx1 - tx0, sent));
        }
        if (send_frame(cfd, &w, ls_now_us() + 3000000u) == LSR_OK)
            ls_read_frame(cfd, fb, sizeof(fb), ls_now_us() + 3000000u, &type, &pl, &plen);
    }
    for (i = 0; i < nd; i++)
        if (data[i] >= 0)
            close(data[i]);
    close(cfd);
}

/* ---- initiator ---- */

static int connect_bound(const cmd_t *c, uint64_t deadline, uint8_t *status)
{
    struct sockaddr_in sa;
    int s = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC | SOCK_NONBLOCK, 0);
    int err = 0;
    socklen_t el = sizeof(err);
    if (s < 0) {
        *status = ST_INTERNAL;
        return -1;
    }
    if (c->dev[0] && setsockopt(s, SOL_SOCKET, SO_BINDTODEVICE, c->dev, (socklen_t)strlen(c->dev)) < 0) {
        *status = ST_NO_DEVICE;
        close(s);
        return -1;
    }
    memset(&sa, 0, sizeof(sa));
    sa.sin_family = AF_INET;
    sa.sin_addr.s_addr = c->src;
    if (c->src && bind(s, (struct sockaddr *)&sa, sizeof(sa)) < 0) {
        *status = ST_NO_DEVICE;
        close(s);
        return -1;
    }
    sa.sin_addr.s_addr = c->dst;
    sa.sin_port = htons(c->port ? c->port : g_conf.data_port);
    if (connect(s, (struct sockaddr *)&sa, sizeof(sa)) < 0 && errno != EINPROGRESS) {
        err = errno;
    } else {
        struct pollfd p;
        uint64_t now = ls_now_us();
        p.fd = s;
        p.events = POLLOUT;
        if (now >= deadline || poll(&p, 1, (int)((deadline - now) / 1000) + 1) <= 0)
            err = ETIMEDOUT;
        else if (getsockopt(s, SOL_SOCKET, SO_ERROR, &err, &el) < 0)
            err = errno;
    }
    if (err) {
        *status = err == ECONNREFUSED ? ST_REFUSED
                  : err == ETIMEDOUT || err == EHOSTUNREACH || err == ENETUNREACH ? ST_UNREACHABLE
                                                                                   : ST_PROTOCOL;
        close(s);
        return -1;
    }
    sock_setup(s);
    return s;
}

/* Performs HELLO/CHALLENGE/AUTH/READY. Returns ST_OK or a failure status. */
static uint8_t handshake(int fd, const cmd_t *c, uint8_t role, uint32_t *session, uint8_t idx)
{
    static uint8_t fb[FBUF];
    uint8_t n1[LSTP_NONCE_LEN], mac[LS_SHA256_LEN], type;
    const uint8_t *pl, *n2;
    size_t plen, n2l;
    uint64_t dl = ls_now_us() + HS_TIMEOUT_US;
    int rc;
    ls_wr w;
    ls_random(n1, sizeof(n1));
    ls_wr_init(&w, fb, sizeof(fb));
    ls_frame_begin(&w, LS_HELLO);
    ls_put_str(&w, T_AGENT_ID, g_conf.id);
    ls_put_u32(&w, T_RUN_ID, c->run_id);
    ls_put_u8(&w, T_TEST_KIND, c->test_kind);
    ls_put_u8(&w, T_DIRECTION, c->direction);
    ls_put_u8(&w, T_STREAMS, c->streams);
    ls_put_u32(&w, T_DURATION_MS, c->duration_ms);
    ls_put(&w, T_NONCE, n1, sizeof(n1));
    ls_put_u8(&w, T_ROLE, role);
    ls_put_u32(&w, T_SESSION, *session);
    ls_put_u8(&w, T_STREAM_IDX, idx);
    if (send_frame(fd, &w, dl) != LSR_OK)
        return ST_PROTOCOL;
    rc = ls_read_frame(fd, fb, sizeof(fb), dl, &type, &pl, &plen);
    if (rc == LSR_TIMEOUT)
        return ST_TIMEOUT;
    if (rc != LSR_OK)
        return role == ROLE_CTRL ? ST_PROTOCOL : ST_AUTH_FAIL;
    if (type == LS_ERROR)
        return ST_AUTH_FAIL;
    if (type != LS_CHALLENGE || !ls_get(pl, plen, T_NONCE, &n2, &n2l) || n2l != LSTP_NONCE_LEN)
        return ST_PROTOCOL;
    auth_mac(c->token, n1, n2, mac);
    ls_wr_init(&w, fb, sizeof(fb));
    ls_frame_begin(&w, LS_AUTH);
    ls_put(&w, T_MAC, mac, sizeof(mac));
    if (send_frame(fd, &w, dl) != LSR_OK)
        return ST_PROTOCOL;
    rc = ls_read_frame(fd, fb, sizeof(fb), dl, &type, &pl, &plen);
    if (rc == LSR_TIMEOUT)
        return ST_TIMEOUT;
    if (rc != LSR_OK || type == LS_ERROR) {
        if (rc == LSR_OK) {
            uint8_t code = 0;
            ls_get_u8(pl, plen, T_ERR_CODE, &code);
            return code == E_LIMIT ? ST_LIMIT : code == E_UNSUPPORTED ? ST_UNSUPPORTED : ST_AUTH_FAIL;
        }
        return ST_AUTH_FAIL;
    }
    if (type != LS_READY || !ls_get_u32(pl, plen, T_SESSION, session))
        return ST_PROTOCOL;
    return ST_OK;
}

static void run_echo(int fd, const cmd_t *c, result_t *r)
{
    static uint8_t fb[FBUF];
    int count = c->count ? c->count : 10, i;
    uint64_t sum = 0;
    r->rtt_min = 0xffffffffu;
    for (i = 0; i < count; i++) {
        ls_wr w;
        uint8_t type;
        const uint8_t *pl;
        size_t plen;
        uint32_t seq = 0;
        uint64_t t0 = ls_now_us(), rtt;
        ls_wr_init(&w, fb, sizeof(fb));
        ls_frame_begin(&w, LS_ECHO_REQ);
        ls_put_u32(&w, T_SEQ, (uint32_t)i);
        ls_put_u64(&w, T_TS_US, t0);
        if (send_frame(fd, &w, t0 + 1000000u) != LSR_OK)
            break;
        r->sent++;
        if (ls_read_frame(fd, fb, sizeof(fb), t0 + 1000000u, &type, &pl, &plen) != LSR_OK ||
            type != LS_ECHO_REP || !ls_get_u32(pl, plen, T_SEQ, &seq) || seq != (uint32_t)i)
            break;
        rtt = ls_now_us() - t0;
        r->recv++;
        sum += rtt;
        if (rtt < r->rtt_min)
            r->rtt_min = (uint32_t)rtt;
        if (rtt > r->rtt_max)
            r->rtt_max = (uint32_t)rtt;
    }
    if (r->recv) {
        r->rtt_avg = (uint32_t)(sum / r->recv);
        if (r->recv < r->sent)
            r->status = ST_TIMEOUT;
    } else {
        r->rtt_min = 0;
        r->status = ST_TIMEOUT;
    }
}

static void run_tcp(int cfd, const cmd_t *c, result_t *r, uint32_t session)
{
    static uint8_t fb[FBUF];
    int data[MAX_STREAMS];
    int n = c->streams ? c->streams : 1, i, opened = 0;
    uint64_t rx0 = 0, tx0 = 0, rx1 = 0, tx1 = 0;
    uint8_t type;
    const uint8_t *pl;
    size_t plen;
    ls_cpu c0, c1;
    ls_wr w;

    if (n > MAX_STREAMS)
        n = MAX_STREAMS;
    for (i = 0; i < n; i++) {
        uint32_t s = session;
        uint8_t st;
        data[i] = connect_bound(c, ls_now_us() + HS_TIMEOUT_US, &st);
        if (data[i] < 0) {
            r->status = st;
            goto out;
        }
        opened++;
        st = handshake(data[i], c, ROLE_DATA, &s, (uint8_t)i);
        if (st != ST_OK) {
            r->status = st;
            goto out;
        }
    }
    ls_if_counters(c->dev, &rx0, &tx0);
    ls_cpu_read(&c0);
    ls_wr_init(&w, fb, sizeof(fb));
    ls_frame_begin(&w, LS_START);
    if (send_frame(cfd, &w, ls_now_us() + 1000000u) != LSR_OK) {
        r->status = ST_PROTOCOL;
        goto out;
    }
    if (c->direction == DIR_FORWARD) {
        uint64_t sent = send_loop(data, n, ls_now_us() + (uint64_t)(c->duration_ms + LSTP_WARMUP_MS) * 1000u);
        uint64_t peer_rx = 0;
        uint16_t cpu = 0;
        uint8_t pok = 1;
        ls_cpu_read(&c1);
        if (ls_read_frame(cfd, fb, sizeof(fb), ls_now_us() + 8000000u, &type, &pl, &plen) != LSR_OK ||
            type != LS_RESULT) {
            r->status = ST_TIMEOUT;
            goto out;
        }
        /* the receiver answers after the last byte arrived: everything left the interface */
        drain_wait(data, n, ls_now_us() + 2000000u);
        ls_if_counters(c->dev, &rx1, &tx1);
        ls_get_u64(pl, plen, T_BYTES, &r->bytes);
        ls_get_u64(pl, plen, T_WINDOW_US, &r->window_us);
        ls_get_u16(pl, plen, T_CPU, &cpu);
        ls_get_u64(pl, plen, T_IF_RX, &peer_rx);
        ls_get_u8(pl, plen, T_PATH_OK, &pok);
        r->peer_cpu = cpu;
        r->if_delta = tx1 - tx0;
        r->path_ok = (uint8_t)(pct90_ok(tx1 - tx0, sent) && pok);
    } else {
        rx_stat st;
        uint16_t cpu = 0;
        uint8_t pok = 1;
        recv_loop(data, n, ls_now_us() + (uint64_t)(c->duration_ms + LSTP_WARMUP_MS) * 1000u + 3000000u,
                  LSTP_WARMUP_MS, &st);
        ls_cpu_read(&c1);
        ls_if_counters(c->dev, &rx1, &tx1);
        r->bytes = st.counted;
        r->window_us = st.window_us;
        if (ls_read_frame(cfd, fb, sizeof(fb), ls_now_us() + 5000000u, &type, &pl, &plen) == LSR_OK &&
            type == LS_SENDER_STAT) {
            ls_get_u16(pl, plen, T_CPU, &cpu);
            ls_get_u8(pl, plen, T_PATH_OK, &pok);
        }
        r->peer_cpu = cpu;
        r->if_delta = rx1 - rx0;
        r->path_ok = (uint8_t)(pct90_ok(rx1 - rx0, st.total) && pok);
    }
    r->local_cpu = (uint16_t)ls_cpu_permille(&c0, &c1);
    if (r->window_us)
        r->bps = r->bytes * 8u * 1000000u / r->window_us;
    if (!r->bytes)
        r->status = ST_TIMEOUT;
    else if (!r->path_ok)
        r->status = ST_COUNTER_MISMATCH;
out:
    for (i = 0; i < opened; i++)
        if (data[i] >= 0)
            close(data[i]);
}

void dp_run(const cmd_t *c, result_t *r)
{
    static uint8_t fb[FBUF];
    uint32_t session = 0;
    uint8_t st;
    ls_wr w;
    int fd = connect_bound(c, ls_now_us() + HS_TIMEOUT_US, &r->status);
    if (fd < 0)
        return;
    st = handshake(fd, c, ROLE_CTRL, &session, 0);
    if (st != ST_OK) {
        r->status = st;
        close(fd);
        return;
    }
    if (c->test_kind == TK_ECHO)
        run_echo(fd, c, r);
    else if (c->test_kind == TK_TCP_THROUGHPUT)
        run_tcp(fd, c, r, session);
    else
        r->status = ST_UNSUPPORTED;
    ls_wr_init(&w, fb, sizeof(fb));
    ls_frame_begin(&w, LS_BYE);
    send_frame(fd, &w, ls_now_us() + 500000u);
    close(fd);
}
