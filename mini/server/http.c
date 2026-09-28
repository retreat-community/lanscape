/* HTTP/1.1 server: embedded page, JSON API and SSE progress stream. */
#include "server.h"
#include "httpparse.h"
#include "../common/jw.h"
#include "../common/util.h"

#include "index_html_gz.h"

#include <errno.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

#define MAX_CLIENTS 32
#define IN_CAP 8192
#define OUT_MAX (1u << 20)

typedef struct {
    int fd;
    char in[IN_CAP];
    size_t inlen;
    char *out;
    size_t outlen;
    size_t outoff;
    int sse;
    int done;
    uint64_t last;
} client_t;

static client_t clients[MAX_CLIENTS];
static char basic_auth[256];
static uint64_t last_ping;

static void b64(const char *in, size_t n, char *out, size_t cap)
{
    static const char t[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    size_t i, o = 0;
    for (i = 0; i < n && o + 5 < cap; i += 3) {
        uint32_t v = (uint32_t)(unsigned char)in[i] << 16;
        if (i + 1 < n)
            v |= (uint32_t)(unsigned char)in[i + 1] << 8;
        if (i + 2 < n)
            v |= (unsigned char)in[i + 2];
        out[o++] = t[v >> 18 & 63];
        out[o++] = t[v >> 12 & 63];
        out[o++] = i + 1 < n ? t[v >> 6 & 63] : '=';
        out[o++] = i + 2 < n ? t[v & 63] : '=';
    }
    out[o] = 0;
}

static void close_client(client_t *c)
{
    if (c->fd >= 0)
        close(c->fd);
    free(c->out);
    memset(c, 0, sizeof(*c));
    c->fd = -1;
}

void http_accept(int lfd)
{
    int fd = accept(lfd, NULL, NULL), i;
    if (fd < 0)
        return;
    for (i = 0; i < MAX_CLIENTS; i++)
        if (clients[i].fd < 0) {
            memset(&clients[i], 0, sizeof(clients[i]));
            clients[i].fd = fd;
            clients[i].last = ls_now_us();
            ls_set_nonblock(fd);
            return;
        }
    close(fd);
}

int http_pollfds(struct pollfd *p, int max)
{
    int i, n = 0;
    for (i = 0; i < MAX_CLIENTS && n < max; i++) {
        if (clients[i].fd < 0)
            continue;
        p[n].fd = clients[i].fd;
        p[n].events = (short)(clients[i].outoff < clients[i].outlen ? POLLOUT : POLLIN);
        p[n].revents = 0;
        n++;
    }
    return n;
}

static int append(client_t *c, const char *s, size_t n)
{
    char *nb;
    if (c->outlen + n > OUT_MAX)
        return -1;
    nb = realloc(c->out, c->outlen + n);
    if (!nb)
        return -1;
    memcpy(nb + c->outlen, s, n);
    c->out = nb;
    c->outlen += n;
    return 0;
}

static void respond(client_t *c, int code, const char *ctype, const char *body, size_t len, int gz)
{
    char h[320];
    const char *reason = code == 200 ? "OK" : code == 202 ? "Accepted" : code == 400 ? "Bad Request"
                       : code == 401 ? "Unauthorized" : code == 404 ? "Not Found"
                       : code == 405 ? "Method Not Allowed" : code == 409 ? "Conflict" : "Error";
    size_t n = ls_fmt(h, sizeof(h),
                      "HTTP/1.1 %d %s\r\nContent-Type: %s\r\nContent-Length: %U\r\n%s%sCache-Control: no-store\r\n"
                      "X-Content-Type-Options: nosniff\r\nConnection: close\r\n\r\n",
                      code, reason, ctype, (uint64_t)len, gz ? "Content-Encoding: gzip\r\n" : "",
                      code == 401 ? "WWW-Authenticate: Basic realm=\"lanscape\"\r\n" : "");
    if (append(c, h, n) || append(c, body, len))
        c->done = 2;
    else
        c->done = 1;
}

static void respond_jw(client_t *c, int code, jw *w)
{
    size_t len;
    char *s = jw_take(w, &len);
    if (!s) {
        respond(c, 500, "application/json", "{\"error\":\"oom\"}", 15, 0);
        return;
    }
    respond(c, code, "application/json", s, len, 0);
    free(s);
}

static void error_json(client_t *c, int code, const char *msg)
{
    jw w;
    jw_init(&w);
    jw_obj(&w);
    jw_kstr(&w, "error", msg);
    jw_end_obj(&w);
    respond_jw(c, code, &w);
}

static void api_state(client_t *c)
{
    const agent_info *ptrs[MAX_AGENTS];
    segment_t *segs = calloc(MAX_SEGS, sizeof(segment_t));
    size_t ll = 0;
    int i, n = 0, nseg;
    jw w;
    if (!segs) {
        error_json(c, 500, "oom");
        return;
    }
    jw_init(&w);
    jw_obj(&w);
    jw_kstr(&w, "version", LS_VERSION);
    jw_kstr(&w, "edition", "mini");
    jw_kuint(&w, "now", ls_wall_ms());
    run_state_json(&w);
    jw_key(&w, "last_run");
    if (store_last(&ll))
        jw_uint(&w, store_last_id());
    else
        jw_null(&w);
    jw_key(&w, "agents");
    jw_arr(&w);
    for (i = 0; i < MAX_AGENTS; i++) {
        const agent_t *a = &g_agents[i];
        if (!a->used || !a->authed)
            continue;
        w_agent(&w, &a->info, a->fd >= 0, a->seen_ms);
        if (a->fd >= 0)
            ptrs[n++] = &a->info;
    }
    jw_end_arr(&w);
    nseg = build_segments(ptrs, n, segs, MAX_SEGS);
    jw_key(&w, "segments");
    w_segments(&w, segs, nseg, ptrs);
    jw_end_obj(&w);
    free(segs);
    respond_jw(c, 200, &w);
}

static void route(client_t *c, const http_req *r)
{
    int get = !strcmp(r->method, "GET"), post = !strcmp(r->method, "POST");
    char path[256];
    char *q;
    ls_strlcpy(path, r->path, sizeof(path));
    q = strchr(path, '?');
    if (q)
        *q = 0;
    if (post && basic_auth[0] && strcmp(r->auth, basic_auth)) {
        respond(c, 401, "application/json", "{\"error\":\"unauthorized\"}", 24, 0);
        return;
    }
    if (get && (!strcmp(path, "/") || !strcmp(path, "/index.html"))) {
        respond(c, 200, "text/html; charset=utf-8", (const char *)index_html_gz, sizeof(index_html_gz), 1);
    } else if (get && !strcmp(path, "/healthz")) {
        respond(c, 200, "text/plain", "ok\n", 3, 0);
    } else if (get && !strcmp(path, "/api/state")) {
        api_state(c);
    } else if (get && !strcmp(path, "/api/last")) {
        size_t n;
        const char *s = store_last(&n);
        if (s)
            respond(c, 200, "application/json", s, n, 0);
        else
            respond(c, 200, "application/json", "null", 4, 0);
    } else if (get && !strcmp(path, "/api/runs")) {
        jw w;
        jw_init(&w);
        store_list_json(&w);
        respond_jw(c, 200, &w);
    } else if (get && !strncmp(path, "/api/runs/", 10)) {
        uint32_t id;
        size_t n;
        const char *s;
        if (ls_parse_u32(path + 10, &id) || !(s = store_get(id, &n)))
            error_json(c, 404, "run not found");
        else
            respond(c, 200, "application/json", s, n, 0);
    } else if (post && !strcmp(path, "/api/run")) {
        int id = run_start();
        if (id == -1) {
            error_json(c, 409, "a run is already in progress");
        } else if (id == -3) {
            error_json(c, 400, "no paths: need at least two agents in a common segment");
        } else if (id < 0) {
            error_json(c, 500, "cannot start run");
        } else {
            jw w;
            jw_init(&w);
            jw_obj(&w);
            jw_kint(&w, "id", id);
            jw_end_obj(&w);
            respond_jw(c, 202, &w);
        }
    } else if (post && !strcmp(path, "/api/cancel")) {
        run_cancel();
        respond(c, 200, "application/json", "{}", 2, 0);
    } else if (get && !strcmp(path, "/api/events")) {
        static const char h[] = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nCache-Control: no-store\r\n"
                                "Connection: keep-alive\r\n\r\nretry: 2000\n\n";
        c->sse = 1;
        if (append(c, h, sizeof(h) - 1))
            c->done = 2;
    } else if (!get && !post) {
        error_json(c, 405, "method not allowed");
    } else {
        error_json(c, 404, "not found");
    }
}

void http_handle(int fd, short revents)
{
    client_t *c = NULL;
    int i;
    for (i = 0; i < MAX_CLIENTS; i++)
        if (clients[i].fd == fd)
            c = &clients[i];
    if (!c)
        return;
    if (revents & (POLLERR | POLLNVAL)) {
        close_client(c);
        return;
    }
    if ((revents & POLLOUT) && c->outoff < c->outlen) {
        ssize_t n = send(fd, c->out + c->outoff, c->outlen - c->outoff, MSG_NOSIGNAL);
        if (n < 0 && errno != EAGAIN && errno != EINTR) {
            close_client(c);
            return;
        }
        if (n > 0) {
            c->outoff += (size_t)n;
            c->last = ls_now_us();
        }
        if (c->outoff == c->outlen) {
            free(c->out);
            c->out = NULL;
            c->outlen = c->outoff = 0;
            if (!c->sse)
                close_client(c);
        }
        return;
    }
    if (revents & (POLLIN | POLLHUP)) {
        http_req r;
        long hl;
        ssize_t n = recv(fd, c->in + c->inlen, IN_CAP - c->inlen - 1, 0);
        if (n == 0 || (n < 0 && errno != EAGAIN && errno != EINTR)) {
            close_client(c);
            return;
        }
        if (n < 0 || c->sse)
            return;
        c->inlen += (size_t)n;
        c->last = ls_now_us();
        if (c->done)
            return;
        hl = http_parse(c->in, c->inlen, &r);
        if (hl < 0 || (hl == 0 && c->inlen >= IN_CAP - 1)) {
            respond(c, 400, "application/json", "{\"error\":\"bad request\"}", 23, 0);
            return;
        }
        if (hl == 0 || r.content_length > (long)(IN_CAP - 1) - hl)
            return;
        if ((long)c->inlen - hl < r.content_length)
            return;
        route(c, &r);
        if (c->done == 2)
            close_client(c);
    }
}

void sse_broadcast(const char *event, const char *data, size_t len)
{
    char h[64];
    int i;
    size_t hn = ls_fmt(h, sizeof(h), "event: %s\ndata: ", event);
    for (i = 0; i < MAX_CLIENTS; i++) {
        client_t *c = &clients[i];
        if (c->fd < 0 || !c->sse)
            continue;
        if (append(c, h, hn) || append(c, data, len) || append(c, "\n\n", 2))
            close_client(c);
    }
}

void http_tick(void)
{
    uint64_t now = ls_now_us();
    int i;
    int ping = now - last_ping > 15000000u;
    if (ping)
        last_ping = now;
    for (i = 0; i < MAX_CLIENTS; i++) {
        client_t *c = &clients[i];
        if (c->fd < 0)
            continue;
        if (c->sse) {
            if (ping && append(c, ": ping\n\n", 8))
                close_client(c);
        } else if (now - c->last > 30000000u) {
            close_client(c);
        }
    }
}

void http_init(void)
{
    int i;
    for (i = 0; i < MAX_CLIENTS; i++)
        clients[i].fd = -1;
}

void http_set_password(const char *pw)
{
    char up[200], enc[260];
    if (!pw[0]) {
        basic_auth[0] = 0;
        return;
    }
    ls_fmt(up, sizeof(up), "admin:%s", pw);
    b64(up, strlen(up), enc, sizeof(enc));
    ls_fmt(basic_auth, sizeof(basic_auth), "Basic %s", enc);
}
