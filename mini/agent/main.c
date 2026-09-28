/* lsm-agent: Lanscape Mini agent. Reports interfaces and runs tests on
 * command from lsm-server (or a Lanscape Full server). */
#include "agent.h"
#include "../common/netif.h"
#include "../common/sha256.h"
#include "../common/tlv.h"
#include "../common/util.h"

#include <errno.h>
#include <fcntl.h>
#include <netdb.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <poll.h>
#include <signal.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/utsname.h>
#include <unistd.h>

#if defined(__x86_64__)
#define ARCH "amd64"
#elif defined(__i386__)
#define ARCH "386"
#elif defined(__aarch64__)
#define ARCH "arm64"
#elif defined(__arm__)
#define ARCH "arm"
#elif defined(__mips64)
#define ARCH "mips64"
#elif defined(__mips__)
#define ARCH "mips"
#elif defined(__riscv)
#define ARCH "riscv64"
#elif defined(__powerpc64__)
#define ARCH "ppc64le"
#else
#define ARCH "unknown"
#endif

agent_conf g_conf;

static uint8_t rbuf[16384];
static size_t rlen;
static uint8_t wbuf[16384];

static void conf_set(const char *k, const char *v, void *ctx)
{
    uint32_t n;
    (void)ctx;
    if (!strcmp(k, "server")) {
        const char *c = strrchr(v, ':');
        size_t hl = c ? (size_t)(c - v) : strlen(v);
        if (hl >= sizeof(g_conf.server))
            hl = sizeof(g_conf.server) - 1;
        memcpy(g_conf.server, v, hl);
        g_conf.server[hl] = 0;
        if (c && !ls_parse_u32(c + 1, &n) && n && n < 65536)
            g_conf.server_port = (uint16_t)n;
    } else if (!strcmp(k, "token")) {
        ls_strlcpy(g_conf.token, v, sizeof(g_conf.token));
    } else if (!strcmp(k, "id")) {
        ls_strlcpy(g_conf.id, v, sizeof(g_conf.id));
    } else if (!strcmp(k, "exclude")) {
        ls_strlcpy(g_conf.exclude, v, sizeof(g_conf.exclude));
    } else if (!strcmp(k, "data_port") && !ls_parse_u32(v, &n) && n && n < 65536) {
        g_conf.data_port = (uint16_t)n;
    } else if (!strcmp(k, "max_duration_ms") && !ls_parse_u32(v, &n)) {
        g_conf.max_duration_ms = n;
    } else if (!strcmp(k, "max_streams") && !ls_parse_u32(v, &n)) {
        g_conf.max_streams = n;
    } else if (!strcmp(k, "inventory_interval") && !ls_parse_u32(v, &n) && n >= 10) {
        g_conf.inventory_s = n;
    } else if (!strcmp(k, "log_level") && !ls_parse_u32(v, &n) && n <= LOG_D) {
        ls_log_level = (int)n;
    }
}

static int dial(void)
{
    struct addrinfo hints, *res, *ai;
    char port[8];
    int fd = -1;
    memset(&hints, 0, sizeof(hints));
    hints.ai_family = AF_INET;
    hints.ai_socktype = SOCK_STREAM;
    ls_fmt(port, sizeof(port), "%u", g_conf.server_port);
    if (getaddrinfo(g_conf.server, port, &hints, &res))
        return -1;
    for (ai = res; ai; ai = ai->ai_next) {
        fd = socket(ai->ai_family, ai->ai_socktype | SOCK_CLOEXEC, ai->ai_protocol);
        if (fd < 0)
            continue;
        if (connect(fd, ai->ai_addr, ai->ai_addrlen) == 0)
            break;
        close(fd);
        fd = -1;
    }
    freeaddrinfo(res);
    if (fd >= 0) {
        int one = 1;
        setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof(one));
        setsockopt(fd, SOL_SOCKET, SO_KEEPALIVE, &one, sizeof(one));
        ls_set_nonblock(fd);
    }
    return fd;
}

static int send_w(int fd, ls_wr *w)
{
    size_t n = ls_frame_end(w);
    if (!n)
        return -1;
    return ls_write_all(fd, w->p + w->frame, n, ls_now_us() + 5000000u) == LSR_OK ? 0 : -1;
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

static void host_id(char *out, size_t cap)
{
    static const char *files[] = {"/etc/machine-id", "/proc/sys/kernel/random/boot_id"};
    unsigned i;
    out[0] = 0;
    for (i = 0; i < 2 && !out[0]; i++) {
        char b[64];
        ssize_t n;
        int fd = open(files[i], O_RDONLY | O_CLOEXEC);
        if (fd < 0)
            continue;
        n = read(fd, b, sizeof(b) - 1);
        close(fd);
        if (n <= 0)
            continue;
        b[n] = 0;
        while (n && (b[n - 1] == '\n' || b[n - 1] == ' '))
            b[--n] = 0;
        ls_strlcpy(out, b, cap);
    }
}

static int handshake(int fd)
{
    uint8_t n1[LSTP_NONCE_LEN], mac[LS_SHA256_LEN], type;
    const uint8_t *pl, *n2, *sm;
    size_t plen, n2l, sml;
    uint64_t dl = ls_now_us() + 5000000u;
    struct utsname u;
    char hid[64];
    ls_wr w;

    ls_random(n1, sizeof(n1));
    uname(&u);
    host_id(hid, sizeof(hid));
    ls_wr_init(&w, wbuf, sizeof(wbuf));
    ls_frame_begin(&w, LC_HELLO);
    ls_put_str(&w, T_AGENT_ID, g_conf.id);
    ls_put_str(&w, T_VERSION, LS_VERSION);
    ls_put(&w, T_NONCE, n1, sizeof(n1));
    ls_put_str(&w, T_HOSTNAME, u.nodename);
    ls_put_str(&w, T_HOST_ID, hid);
    ls_put_str(&w, T_ARCH, ARCH);
    ls_put_u32(&w, T_CAPS, CAP_LITE | CAP_ICMP | CAP_TCP);
    ls_put_u16(&w, T_DST_PORT, g_conf.data_port);
    if (send_w(fd, &w))
        return -1;
    if (ls_read_frame(fd, rbuf, sizeof(rbuf), dl, &type, &pl, &plen) != LSR_OK || type != LC_CHALLENGE ||
        !ls_get(pl, plen, T_NONCE, &n2, &n2l) || n2l != LSTP_NONCE_LEN)
        return -1;
    {
        uint8_t n2c[LSTP_NONCE_LEN];
        memcpy(n2c, n2, sizeof(n2c));
        labelled_mac("agent", n1, n2c, mac);
        ls_wr_init(&w, wbuf, sizeof(wbuf));
        ls_frame_begin(&w, LC_AUTH);
        ls_put(&w, T_MAC, mac, sizeof(mac));
        if (send_w(fd, &w))
            return -1;
        if (ls_read_frame(fd, rbuf, sizeof(rbuf), dl, &type, &pl, &plen) != LSR_OK || type != LC_WELCOME ||
            !ls_get(pl, plen, T_SERVER_MAC, &sm, &sml) || sml != LS_SHA256_LEN)
            return -1;
        labelled_mac("server", n2c, n1, mac);
        if (!ls_ct_equal(mac, sm, LS_SHA256_LEN)) {
            ls_log(LOG_E, "server failed to prove the shared token");
            return -1;
        }
    }
    return 0;
}

static uint32_t inv_hash;

static int send_inventory(int fd, int force)
{
    static ls_if ifs[LS_MAX_IFS];
    uint8_t h[LS_SHA256_LEN];
    uint32_t hv;
    int n = ls_if_list(ifs, LS_MAX_IFS, g_conf.exclude), i;
    ls_wr w;
    if (n < 0)
        n = 0;
    ls_sha256_once(ifs, sizeof(ifs[0]) * (size_t)n, h);
    hv = ls_be32(h);
    if (!force && hv == inv_hash)
        return 0;
    inv_hash = hv;
    ls_wr_init(&w, wbuf, sizeof(wbuf));
    ls_frame_begin(&w, LC_INVENTORY);
    for (i = 0; i < n; i++) {
        size_t off = ls_nest_begin(&w, T_IFACE);
        ls_put_str(&w, T_IF_NAME, ifs[i].name);
        ls_put(&w, T_IF_MAC, ifs[i].mac, 6);
        ls_put(&w, T_IF_ADDR4, &ifs[i].addr, 4);
        ls_put_u8(&w, T_IF_PREFIX, (uint8_t)ifs[i].prefix);
        ls_put_u16(&w, T_IF_VLAN, ifs[i].vlan);
        ls_put_u32(&w, T_IF_SPEED, ifs[i].speed);
        ls_put_u32(&w, T_IF_MTU, ifs[i].mtu);
        ls_put_u32(&w, T_IF_FLAGS, ifs[i].flags);
        if (ifs[i].kind[0])
            ls_put_str(&w, T_IF_KIND, ifs[i].kind);
        if (ifs[i].parent[0])
            ls_put_str(&w, T_IF_PARENT, ifs[i].parent);
        ls_nest_end(&w, off);
    }
    ls_log(LOG_D, "inventory: %d interfaces", n);
    return send_w(fd, &w);
}

static void parse_cmd(const uint8_t *pl, size_t plen, cmd_t *c)
{
    const uint8_t *v;
    size_t vl;
    memset(c, 0, sizeof(*c));
    ls_get_u32(pl, plen, T_CMD_ID, &c->cmd_id);
    ls_get_u8(pl, plen, T_CMD_KIND, &c->kind);
    ls_get_str(pl, plen, T_SRC_DEV, c->dev, sizeof(c->dev));
    if (ls_get(pl, plen, T_SRC_ADDR, &v, &vl) && vl == 4)
        memcpy(&c->src, v, 4);
    if (ls_get(pl, plen, T_DST_ADDR, &v, &vl) && vl == 4)
        memcpy(&c->dst, v, 4);
    ls_get_u16(pl, plen, T_COUNT, &c->count);
    ls_get_u16(pl, plen, T_INTERVAL_MS, &c->interval_ms);
    ls_get_u16(pl, plen, T_SIZE, &c->size);
    ls_get_u16(pl, plen, T_DST_PORT, &c->port);
    ls_get_u32(pl, plen, T_RUN_ID, &c->run_id);
    if (ls_get(pl, plen, T_RUN_TOKEN, &v, &vl) && vl == LSTP_TOKEN_LEN)
        memcpy(c->token, v, LSTP_TOKEN_LEN);
    ls_get_u8(pl, plen, T_TEST_KIND, &c->test_kind);
    ls_get_u8(pl, plen, T_DIRECTION, &c->direction);
    ls_get_u8(pl, plen, T_STREAMS, &c->streams);
    ls_get_u32(pl, plen, T_DURATION_MS, &c->duration_ms);
}

static int exec_cmd(int fd, const uint8_t *pl, size_t plen)
{
    cmd_t c;
    result_t r;
    ls_wr w;
    parse_cmd(pl, plen, &c);
    memset(&r, 0, sizeof(r));
    r.path_ok = 1;
    if (c.dev[0] && ls_ifindex(c.dev) < 0) {
        r.status = ST_NO_DEVICE;
    } else if (c.kind == CK_LSTP && (c.duration_ms > g_conf.max_duration_ms || c.streams > g_conf.max_streams)) {
        r.status = ST_LIMIT;
    } else {
        if (ls_route_dev(c.dst, r.route_dev, sizeof(r.route_dev)) == 0 && c.dev[0] &&
            strcmp(r.route_dev, c.dev)) {
            r.status = ST_ROUTE_MISMATCH;
            r.path_ok = 0;
        } else if (c.kind == CK_PING) {
            icmp_run(&c, &r, 0);
        } else if (c.kind == CK_PMTU) {
            icmp_run(&c, &r, 1);
        } else if (c.kind == CK_LSTP) {
            dp_run(&c, &r);
        } else {
            r.status = ST_UNSUPPORTED;
        }
    }
    ls_log(LOG_D, "cmd %u kind=%u dst=%I status=%u", c.cmd_id, c.kind, c.dst, r.status);
    ls_wr_init(&w, wbuf, sizeof(wbuf));
    ls_frame_begin(&w, LC_CMD_RESULT);
    ls_put_u32(&w, T_CMD_ID, c.cmd_id);
    ls_put_u8(&w, T_STATUS, r.status);
    if (r.route_dev[0])
        ls_put_str(&w, T_ROUTE_DEV, r.route_dev);
    ls_put_u16(&w, T_SENT, r.sent);
    ls_put_u16(&w, T_RECV, r.recv);
    ls_put_u32(&w, T_RTT_MIN_US, r.rtt_min);
    ls_put_u32(&w, T_RTT_AVG_US, r.rtt_avg);
    ls_put_u32(&w, T_RTT_MAX_US, r.rtt_max);
    ls_put_u64(&w, T_BYTES, r.bytes);
    ls_put_u64(&w, T_WINDOW_US, r.window_us);
    ls_put_u64(&w, T_BPS, r.bps);
    ls_put_u16(&w, T_LOCAL_CPU, r.local_cpu);
    ls_put_u16(&w, T_PEER_CPU, r.peer_cpu);
    ls_put_u8(&w, T_PATH_OK, r.path_ok);
    ls_put_u64(&w, T_IF_TX, r.if_delta);
    if (r.msg[0])
        ls_put_str(&w, T_ERR_MSG, r.msg);
    return send_w(fd, &w);
}

static int handle(int fd, uint8_t type, const uint8_t *pl, size_t plen)
{
    ls_wr w;
    if (type == LC_PING) {
        ls_wr_init(&w, wbuf, sizeof(wbuf));
        ls_frame_begin(&w, LC_PONG);
        return send_w(fd, &w);
    }
    if (type == LC_GRANT) {
        uint32_t run_id = 0, ttl = LSTP_TOKEN_TTL_S;
        const uint8_t *t;
        size_t tl;
        if (!ls_get_u32(pl, plen, T_RUN_ID, &run_id) || !ls_get(pl, plen, T_RUN_TOKEN, &t, &tl) ||
            tl != LSTP_TOKEN_LEN)
            return 0;
        ls_get_u32(pl, plen, T_TTL_S, &ttl);
        dp_grant(run_id, t, ttl);
        ls_wr_init(&w, wbuf, sizeof(wbuf));
        ls_frame_begin(&w, LC_GRANT_ACK);
        ls_put_u32(&w, T_RUN_ID, run_id);
        return send_w(fd, &w);
    }
    if (type == LC_CMD)
        return exec_cmd(fd, pl, plen);
    return 0;
}

static int listen_data(void)
{
    struct sockaddr_in sa;
    int one = 1;
    int s = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (s < 0)
        return -1;
    setsockopt(s, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));
    memset(&sa, 0, sizeof(sa));
    sa.sin_family = AF_INET;
    sa.sin_port = htons(g_conf.data_port);
    if (bind(s, (struct sockaddr *)&sa, sizeof(sa)) < 0 || listen(s, 16) < 0) {
        close(s);
        return -1;
    }
    return s;
}

static void session(int fd, int lfd)
{
    uint64_t last_rx = ls_now_us(), last_tx = last_rx, last_inv = last_rx;
    rlen = 0;
    inv_hash = 0;
    if (send_inventory(fd, 1))
        return;
    for (;;) {
        struct pollfd p[2];
        uint64_t now;
        p[0].fd = fd;
        p[0].events = POLLIN;
        p[0].revents = 0;
        p[1].fd = lfd;
        p[1].events = POLLIN;
        p[1].revents = 0;
        poll(p, 2, 1000);
        if (p[1].revents & POLLIN) {
            int c = accept(lfd, NULL, NULL);
            if (c >= 0)
                dp_respond(c, lfd);
        }
        if (p[0].revents & (POLLIN | POLLHUP | POLLERR)) {
            ssize_t n = read(fd, rbuf + rlen, sizeof(rbuf) - rlen);
            if (n == 0 || (n < 0 && errno != EAGAIN && errno != EINTR))
                return;
            if (n > 0) {
                rlen += (size_t)n;
                last_rx = ls_now_us();
            }
            for (;;) {
                uint8_t type;
                const uint8_t *pl;
                size_t plen;
                long fl = ls_frame_parse(rbuf, rlen, &type, &pl, &plen);
                if (fl < 0 || (fl == 0 && rlen == sizeof(rbuf)))
                    return;
                if (fl == 0)
                    break;
                if (handle(fd, type, pl, plen))
                    return;
                last_tx = ls_now_us();
                memmove(rbuf, rbuf + fl, rlen - (size_t)fl);
                rlen -= (size_t)fl;
            }
        }
        now = ls_now_us();
        if (now - last_rx > 90000000u)
            return;
        if (now - last_inv > 30000000u) {
            /* cheap change detection every 30 s, full resend on the configured interval */
            int force = now - last_tx > (uint64_t)g_conf.inventory_s * 1000000u;
            if (send_inventory(fd, force))
                return;
            last_inv = now;
            if (force)
                last_tx = now;
        }
        if (now - last_tx > 30000000u) {
            ls_wr w;
            ls_wr_init(&w, wbuf, sizeof(wbuf));
            ls_frame_begin(&w, LC_PING);
            if (send_w(fd, &w))
                return;
            last_tx = now;
        }
    }
}

static void usage(void)
{
    static const char u[] =
        "usage: lsm-agent [-c config] [-s host[:port]] [-t token] [-n id] [-x exclude] [-v] [-V]\n";
    if (write(2, u, sizeof(u) - 1) < 0)
        return;
}

int main(int argc, char **argv)
{
    const char *cfg = NULL;
    int i, lfd;
    uint32_t backoff = 1;
    struct utsname u;

    signal(SIGPIPE, SIG_IGN);
    g_conf.server_port = LSTP_CTL_PORT;
    g_conf.data_port = LSTP_DATA_PORT;
    g_conf.max_duration_ms = 30000;
    g_conf.max_streams = 8;
    g_conf.inventory_s = 300;
    ls_strlcpy(g_conf.exclude, "wan*,tailscale*,docker*,veth*,cni*,flannel*,lxc*,virbr*", sizeof(g_conf.exclude));
    if (uname(&u) == 0)
        ls_strlcpy(g_conf.id, u.nodename, sizeof(g_conf.id));

    for (i = 1; i < argc; i++)
        if (!strcmp(argv[i], "-c") && i + 1 < argc)
            cfg = argv[++i];
    if (!cfg && access("/etc/config/lsm", R_OK) == 0)
        cfg = "/etc/config/lsm";
    else if (!cfg && access("/etc/lsm/agent.conf", R_OK) == 0)
        cfg = "/etc/lsm/agent.conf";
    if (cfg && ls_conf_load(cfg, conf_set, NULL))
        ls_log(LOG_W, "cannot read config %s", cfg);
    ls_conf_env("LSM", conf_set, NULL);
    for (i = 1; i < argc; i++) {
        const char *a = argv[i];
        const char *v = i + 1 < argc ? argv[i + 1] : NULL;
        if (!strcmp(a, "-V")) {
            static const char vs[] = "lsm-agent " LS_VERSION "\n";
            return write(1, vs, sizeof(vs) - 1) < 0;
        } else if (!strcmp(a, "-v")) {
            ls_log_level = LOG_D;
        } else if (!strcmp(a, "-c") && v) {
            i++;
        } else if (!strcmp(a, "-s") && v) {
            conf_set("server", v, NULL);
            i++;
        } else if (!strcmp(a, "-t") && v) {
            conf_set("token", v, NULL);
            i++;
        } else if (!strcmp(a, "-n") && v) {
            conf_set("id", v, NULL);
            i++;
        } else if (!strcmp(a, "-x") && v) {
            conf_set("exclude", v, NULL);
            i++;
        } else {
            usage();
            return 2;
        }
    }
    if (!g_conf.server[0] || !g_conf.token[0]) {
        ls_log(LOG_E, "server and token are required");
        usage();
        return 2;
    }
    lfd = listen_data();
    if (lfd < 0) {
        ls_log(LOG_E, "cannot listen on data port %u: errno %d", g_conf.data_port, errno);
        return 1;
    }
    ls_log(LOG_I, "lsm-agent %s id=%s server=%s:%u", LS_VERSION, g_conf.id, g_conf.server, g_conf.server_port);
    for (;;) {
        int fd = dial();
        if (fd >= 0 && handshake(fd) == 0) {
            ls_log(LOG_I, "connected to server");
            backoff = 1;
            session(fd, lfd);
            ls_log(LOG_W, "disconnected from server");
        } else {
            ls_log(LOG_W, "cannot connect to server, retry in %us", backoff);
        }
        if (fd >= 0)
            close(fd);
        sleep(backoff);
        if (backoff < 30)
            backoff *= 2;
    }
}
