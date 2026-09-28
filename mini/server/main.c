/* lsm-server: Lanscape Mini server. */
#include "server.h"
#include "../common/util.h"

#include <errno.h>
#include <netdb.h>
#include <netinet/in.h>
#include <poll.h>
#include <signal.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

server_conf g_conf;

static void add_expect(const char *v)
{
    char item[64];
    while (v && *v) {
        const char *c = strchr(v, ',');
        size_t n = c ? (size_t)(c - v) : strlen(v);
        char *colon;
        uint32_t mbps;
        expect_t *e;
        if (n < sizeof(item) && g_conf.nexpect < MAX_EXPECT) {
            memcpy(item, v, n);
            item[n] = 0;
            colon = strchr(item, ':');
            e = &g_conf.expect[g_conf.nexpect];
            if (colon) {
                *colon = 0;
                if (!ls_parse_cidr(item, &e->net, &e->prefix) && !ls_parse_u32(colon + 1, &mbps)) {
                    e->mbps = mbps;
                    g_conf.nexpect++;
                } else {
                    ls_log(LOG_W, "bad expect entry: %s", item);
                }
            }
        }
        v = c ? c + 1 : NULL;
    }
}

static void conf_set(const char *k, const char *v, void *ctx)
{
    uint32_t n;
    (void)ctx;
    if (!strcmp(k, "listen"))
        ls_strlcpy(g_conf.listen, v, sizeof(g_conf.listen));
    else if (!strcmp(k, "ctl_listen"))
        ls_strlcpy(g_conf.ctl_listen, v, sizeof(g_conf.ctl_listen));
    else if (!strcmp(k, "token"))
        ls_strlcpy(g_conf.token, v, sizeof(g_conf.token));
    else if (!strcmp(k, "data_file"))
        ls_strlcpy(g_conf.data_file, v, sizeof(g_conf.data_file));
    else if (!strcmp(k, "webhook"))
        ls_strlcpy(g_conf.webhook, v, sizeof(g_conf.webhook));
    else if (!strcmp(k, "password"))
        ls_strlcpy(g_conf.password, v, sizeof(g_conf.password));
    else if (!strcmp(k, "expect"))
        add_expect(v);
    else if (ls_parse_u32(v, &n))
        return;
    else if (!strcmp(k, "keep_runs"))
        g_conf.keep_runs = n;
    else if (!strcmp(k, "duration_ms") && n >= 1000 && n <= 30000)
        g_conf.duration_ms = n;
    else if (!strcmp(k, "streams") && n >= 1 && n <= 16)
        g_conf.streams = n;
    else if (!strcmp(k, "ping_count") && n >= 1 && n <= 64)
        g_conf.ping_count = n;
    else if (!strcmp(k, "rtt_warn_ms"))
        g_conf.rtt_warn_ms = n;
    else if (!strcmp(k, "log_level") && n <= LOG_D)
        ls_log_level = (int)n;
}

static int listen_on(const char *addr, uint16_t defport)
{
    struct sockaddr_in sa;
    char host[64];
    const char *c = strrchr(addr, ':');
    uint32_t port = defport;
    int s, one = 1;
    memset(&sa, 0, sizeof(sa));
    sa.sin_family = AF_INET;
    ls_strlcpy(host, addr, sizeof(host));
    if (c) {
        host[c - addr] = 0;
        if (ls_parse_u32(c + 1, &port) || port > 65535)
            return -1;
    }
    if (host[0] && ls_parse_ipv4(host, &sa.sin_addr.s_addr))
        return -1;
    sa.sin_port = htons((uint16_t)port);
    s = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (s < 0)
        return -1;
    setsockopt(s, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));
    if (bind(s, (struct sockaddr *)&sa, sizeof(sa)) < 0 || listen(s, 64) < 0) {
        close(s);
        return -1;
    }
    ls_set_nonblock(s);
    return s;
}

void webhook_post(const char *json, size_t len)
{
    char host[128], port[8] = "80", head[400];
    const char *u = g_conf.webhook, *path, *hp;
    struct addrinfo hints, *res;
    int fd = -1;
    size_t hl;
    if (!u[0])
        return;
    if (strncmp(u, "http://", 7)) {
        ls_log(LOG_W, "webhook: only http:// URLs are supported");
        return;
    }
    u += 7;
    path = strchr(u, '/');
    hl = path ? (size_t)(path - u) : strlen(u);
    if (hl >= sizeof(host))
        return;
    memcpy(host, u, hl);
    host[hl] = 0;
    hp = strchr(host, ':');
    if (hp) {
        ls_strlcpy(port, hp + 1, sizeof(port));
        host[hp - host] = 0;
    }
    memset(&hints, 0, sizeof(hints));
    hints.ai_family = AF_INET;
    hints.ai_socktype = SOCK_STREAM;
    if (getaddrinfo(host, port, &hints, &res)) {
        ls_log(LOG_W, "webhook: cannot resolve %s", host);
        return;
    }
    fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC | SOCK_NONBLOCK, 0);
    if (fd >= 0) {
        uint64_t dl = ls_now_us() + 3000000u;
        int err = 0;
        socklen_t el = sizeof(err);
        struct pollfd p;
        if (connect(fd, res->ai_addr, res->ai_addrlen) < 0 && errno != EINPROGRESS)
            err = errno;
        p.fd = fd;
        p.events = POLLOUT;
        if (!err && (poll(&p, 1, 3000) <= 0 || getsockopt(fd, SOL_SOCKET, SO_ERROR, &err, &el) < 0))
            err = ETIMEDOUT;
        if (!err) {
            size_t n = ls_fmt(head, sizeof(head),
                              "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\n"
                              "User-Agent: lsm-server/%s\r\nContent-Length: %U\r\nConnection: close\r\n\r\n",
                              path ? path : "/", host, LS_VERSION, (uint64_t)len);
            if (ls_write_all(fd, head, n, dl) != LSR_OK || ls_write_all(fd, json, len, dl) != LSR_OK)
                err = EIO;
            else {
                char resp[64];
                ssize_t r;
                p.events = POLLIN;
                if (poll(&p, 1, 3000) > 0 && (r = read(fd, resp, sizeof(resp) - 1)) > 0) {
                    resp[r] = 0;
                    ls_log(LOG_I, "webhook delivered: %s", strtok(resp, "\r"));
                }
            }
        }
        if (err)
            ls_log(LOG_W, "webhook to %s failed: errno %d", host, err);
        close(fd);
    }
    freeaddrinfo(res);
}

static void usage(void)
{
    static const char u[] = "usage: lsm-server [-c config] [-t token] [-l listen] [-d data_file] [-v] [-V]\n";
    if (write(2, u, sizeof(u) - 1) < 0)
        return;
}

int main(int argc, char **argv)
{
    const char *cfg = NULL;
    int hfd, cfd, i;
    static struct pollfd p[2 + MAX_AGENTS + 32];

    signal(SIGPIPE, SIG_IGN);
    ls_strlcpy(g_conf.listen, ":8080", sizeof(g_conf.listen));
    ls_strlcpy(g_conf.ctl_listen, ":47701", sizeof(g_conf.ctl_listen));
    ls_strlcpy(g_conf.data_file, "/var/lib/lsm/runs.json", sizeof(g_conf.data_file));
    g_conf.keep_runs = 20;
    g_conf.duration_ms = 5000;
    g_conf.streams = 4;
    g_conf.ping_count = 10;
    g_conf.rtt_warn_ms = 20;
    for (i = 1; i < argc; i++)
        if (!strcmp(argv[i], "-c") && i + 1 < argc)
            cfg = argv[++i];
    if (!cfg && access("/etc/config/lsm", R_OK) == 0)
        cfg = "/etc/config/lsm";
    else if (!cfg && access("/etc/lsm/server.conf", R_OK) == 0)
        cfg = "/etc/lsm/server.conf";
    if (cfg && ls_conf_load(cfg, conf_set, NULL))
        ls_log(LOG_W, "cannot read config %s", cfg);
    ls_conf_env("LSM", conf_set, NULL);
    for (i = 1; i < argc; i++) {
        const char *a = argv[i], *v = i + 1 < argc ? argv[i + 1] : NULL;
        if (!strcmp(a, "-V")) {
            static const char vs[] = "lsm-server " LS_VERSION "\n";
            return write(1, vs, sizeof(vs) - 1) < 0;
        } else if (!strcmp(a, "-v")) {
            ls_log_level = LOG_D;
        } else if (!strcmp(a, "-c") && v) {
            i++;
        } else if (!strcmp(a, "-t") && v) {
            conf_set("token", v, NULL);
            i++;
        } else if (!strcmp(a, "-l") && v) {
            conf_set("listen", v, NULL);
            i++;
        } else if (!strcmp(a, "-d") && v) {
            conf_set("data_file", v, NULL);
            i++;
        } else {
            usage();
            return 2;
        }
    }
    if (strlen(g_conf.token) < 8) {
        ls_log(LOG_E, "a shared token of at least 8 characters is required");
        usage();
        return 2;
    }
    for (i = 0; i < MAX_AGENTS; i++)
        g_agents[i].fd = -1;
    http_init();
    http_set_password(g_conf.password);
    hfd = listen_on(g_conf.listen, 8080);
    cfd = listen_on(g_conf.ctl_listen, LSTP_CTL_PORT);
    if (hfd < 0 || cfd < 0) {
        ls_log(LOG_E, "cannot listen on %s / %s: errno %d", g_conf.listen, g_conf.ctl_listen, errno);
        return 1;
    }
    store_load();
    ls_log(LOG_I, "lsm-server %s http=%s control=%s", LS_VERSION, g_conf.listen, g_conf.ctl_listen);
    for (;;) {
        int na, nh, n;
        p[0].fd = hfd;
        p[0].events = POLLIN;
        p[0].revents = 0;
        p[1].fd = cfd;
        p[1].events = POLLIN;
        p[1].revents = 0;
        na = agents_pollfds(p + 2, MAX_AGENTS);
        nh = http_pollfds(p + 2 + na, 32);
        n = poll(p, (nfds_t)(2 + na + nh), 250);
        if (n > 0) {
            if (p[0].revents & POLLIN)
                http_accept(hfd);
            if (p[1].revents & POLLIN)
                agents_accept(cfd);
            for (i = 0; i < na; i++)
                if (p[2 + i].revents)
                    agents_handle(p[2 + i].fd, p[2 + i].revents);
            for (i = 0; i < nh; i++)
                if (p[2 + na + i].revents)
                    http_handle(p[2 + na + i].fd, p[2 + na + i].revents);
        }
        agents_tick();
        run_tick();
        http_tick();
    }
}
