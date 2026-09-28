#include "util.h"
#include "lstp.h"
#include "tlv.h"

#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/syscall.h>
#include <time.h>
#include <unistd.h>

extern char **environ;

int ls_log_level = LOG_I;

typedef struct {
    char *b;
    size_t cap;
    size_t n;
} fbuf;

static void fput(fbuf *f, char c)
{
    if (f->n + 1 < f->cap)
        f->b[f->n] = c;
    f->n++;
}

static void fputs_(fbuf *f, const char *s)
{
    while (*s)
        fput(f, *s++);
}

static void fputu(fbuf *f, uint64_t v, unsigned base, int minw)
{
    char t[24];
    int i = 0;
    do {
        unsigned d = (unsigned)(v % base);
        t[i++] = (char)(d < 10 ? '0' + d : 'a' + d - 10);
        v /= base;
    } while (v && i < (int)sizeof(t));
    while (i < minw && i < (int)sizeof(t))
        t[i++] = '0';
    while (i)
        fput(f, t[--i]);
}

size_t ls_vfmt(char *buf, size_t cap, const char *fmt, va_list ap)
{
    fbuf f;
    f.b = buf;
    f.cap = cap;
    f.n = 0;
    for (; *fmt; fmt++) {
        if (*fmt != '%') {
            fput(&f, *fmt);
            continue;
        }
        fmt++;
        switch (*fmt) {
        case 's': {
            const char *s = va_arg(ap, const char *);
            fputs_(&f, s ? s : "(null)");
            break;
        }
        case 'd': {
            int v = va_arg(ap, int);
            if (v < 0) {
                fput(&f, '-');
                fputu(&f, (uint64_t)(-(int64_t)v), 10, 0);
            } else {
                fputu(&f, (uint64_t)v, 10, 0);
            }
            break;
        }
        case 'D': {
            int64_t v = va_arg(ap, int64_t);
            if (v < 0) {
                fput(&f, '-');
                fputu(&f, (uint64_t)0 - (uint64_t)v, 10, 0);
            } else {
                fputu(&f, (uint64_t)v, 10, 0);
            }
            break;
        }
        case 'u':
            fputu(&f, va_arg(ap, unsigned), 10, 0);
            break;
        case 'U':
            fputu(&f, va_arg(ap, uint64_t), 10, 0);
            break;
        case 'x':
            fputu(&f, va_arg(ap, unsigned), 16, 0);
            break;
        case 'h':
            fputu(&f, va_arg(ap, unsigned) & 0xff, 16, 2);
            break;
        case 'c':
            fput(&f, (char)va_arg(ap, int));
            break;
        case 'I': {
            uint32_t a = va_arg(ap, uint32_t);
            const uint8_t *p = (const uint8_t *)&a;
            int i;
            for (i = 0; i < 4; i++) {
                if (i)
                    fput(&f, '.');
                fputu(&f, p[i], 10, 0);
            }
            break;
        }
        case '%':
            fput(&f, '%');
            break;
        case 0:
            fmt--;
            break;
        default:
            fput(&f, '%');
            fput(&f, *fmt);
        }
    }
    if (cap)
        buf[f.n < cap ? f.n : cap - 1] = 0;
    return f.n < cap ? f.n : (cap ? cap - 1 : 0);
}

size_t ls_fmt(char *buf, size_t cap, const char *fmt, ...)
{
    va_list ap;
    size_t n;
    va_start(ap, fmt);
    n = ls_vfmt(buf, cap, fmt, ap);
    va_end(ap);
    return n;
}

void ls_log(int level, const char *fmt, ...)
{
    static const char *lv[] = {"error", "warn", "info", "debug"};
    char line[512];
    size_t n;
    va_list ap;
    if (level > ls_log_level)
        return;
    n = ls_fmt(line, sizeof(line), "level=%s msg=", lv[level]);
    va_start(ap, fmt);
    n += ls_vfmt(line + n, sizeof(line) - n, fmt, ap);
    va_end(ap);
    if (n > sizeof(line) - 2)
        n = sizeof(line) - 2;
    line[n++] = '\n';
    if (write(2, line, n) < 0)
        return;
}

uint64_t ls_now_us(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000000u + (uint64_t)ts.tv_nsec / 1000u;
}

uint64_t ls_wall_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (uint64_t)ts.tv_sec * 1000u + (uint64_t)ts.tv_nsec / 1000000u;
}

int ls_random(void *buf, size_t len)
{
    uint8_t *p = buf;
    int fd;
#ifdef SYS_getrandom
    while (len) {
        long n = syscall(SYS_getrandom, p, len, 0);
        if (n <= 0)
            break;
        p += n;
        len -= (size_t)n;
    }
    if (!len)
        return 0;
#endif
    fd = open("/dev/urandom", O_RDONLY | O_CLOEXEC);
    if (fd < 0)
        return -1;
    while (len) {
        ssize_t n = read(fd, p, len);
        if (n <= 0) {
            close(fd);
            return -1;
        }
        p += n;
        len -= (size_t)n;
    }
    close(fd);
    return 0;
}

void ls_strlcpy(char *dst, const char *src, size_t cap)
{
    size_t n = strlen(src);
    if (!cap)
        return;
    if (n >= cap)
        n = cap - 1;
    memcpy(dst, src, n);
    dst[n] = 0;
}

int ls_set_nonblock(int fd)
{
    int fl = fcntl(fd, F_GETFL, 0);
    if (fl < 0)
        return -1;
    return fcntl(fd, F_SETFL, fl | O_NONBLOCK);
}

int ls_parse_u32(const char *s, uint32_t *out)
{
    uint64_t v = 0;
    if (!*s)
        return -1;
    for (; *s; s++) {
        if (*s < '0' || *s > '9')
            return -1;
        v = v * 10 + (uint64_t)(*s - '0');
        if (v > 0xffffffffu)
            return -1;
    }
    *out = (uint32_t)v;
    return 0;
}

int ls_parse_ipv4(const char *s, uint32_t *out)
{
    uint8_t b[4];
    int i;
    for (i = 0; i < 4; i++) {
        unsigned v = 0;
        int d = 0;
        while (*s >= '0' && *s <= '9' && d < 3) {
            v = v * 10 + (unsigned)(*s++ - '0');
            d++;
        }
        if (!d || v > 255)
            return -1;
        b[i] = (uint8_t)v;
        if (i < 3 && *s++ != '.')
            return -1;
    }
    if (*s)
        return -1;
    memcpy(out, b, 4);
    return 0;
}

uint32_t ls_prefix_mask(int prefix)
{
    uint32_t m = prefix <= 0 ? 0 : prefix >= 32 ? 0xffffffffu : ~((1u << (32 - prefix)) - 1);
    uint8_t b[4];
    uint32_t r;
    ls_wbe32(b, m);
    memcpy(&r, b, 4);
    return r;
}

int ls_parse_cidr(const char *s, uint32_t *net, int *prefix)
{
    char ip[20];
    const char *sl = strchr(s, '/');
    uint32_t p = 32;
    size_t n = sl ? (size_t)(sl - s) : strlen(s);
    if (n >= sizeof(ip))
        return -1;
    memcpy(ip, s, n);
    ip[n] = 0;
    if (ls_parse_ipv4(ip, net))
        return -1;
    if (sl && (ls_parse_u32(sl + 1, &p) || p > 32))
        return -1;
    *prefix = (int)p;
    *net &= ls_prefix_mask((int)p);
    return 0;
}

int ls_write_all(int fd, const void *buf, size_t len, uint64_t deadline)
{
    const uint8_t *p = buf;
    while (len) {
        ssize_t n = write(fd, p, len);
        if (n > 0) {
            p += n;
            len -= (size_t)n;
            continue;
        }
        if (n < 0 && (errno == EAGAIN || errno == EINTR)) {
            struct pollfd pfd;
            uint64_t now = ls_now_us();
            if (now >= deadline)
                return LSR_TIMEOUT;
            pfd.fd = fd;
            pfd.events = POLLOUT;
            poll(&pfd, 1, (int)((deadline - now) / 1000 + 1));
            continue;
        }
        return LSR_ERR;
    }
    return LSR_OK;
}

static int read_exact(int fd, uint8_t *p, size_t len, uint64_t deadline)
{
    while (len) {
        ssize_t n = read(fd, p, len);
        if (n > 0) {
            p += n;
            len -= (size_t)n;
            continue;
        }
        if (n == 0)
            return LSR_EOF;
        if (errno == EAGAIN || errno == EINTR) {
            struct pollfd pfd;
            uint64_t now = ls_now_us();
            if (now >= deadline)
                return LSR_TIMEOUT;
            pfd.fd = fd;
            pfd.events = POLLIN;
            poll(&pfd, 1, (int)((deadline - now) / 1000 + 1));
            continue;
        }
        return LSR_ERR;
    }
    return LSR_OK;
}

int ls_read_frame(int fd, uint8_t *buf, size_t cap, uint64_t deadline, uint8_t *type,
                  const uint8_t **payload, size_t *plen)
{
    size_t n;
    int rc;
    if (cap < LSTP_HDR)
        return LSR_ERR;
    rc = read_exact(fd, buf, LSTP_HDR, deadline);
    if (rc != LSR_OK)
        return rc;
    if (buf[0] != 'L' || buf[1] != 'S' || buf[2] != LSTP_VERSION)
        return LSR_PROTO;
    n = ls_be16(buf + 4);
    if (LSTP_HDR + n > cap)
        return LSR_PROTO;
    rc = read_exact(fd, buf + LSTP_HDR, n, deadline);
    if (rc != LSR_OK)
        return rc == LSR_EOF ? LSR_PROTO : rc;
    if (ls_frame_parse(buf, LSTP_HDR + n, type, payload, plen) <= 0)
        return LSR_PROTO;
    return LSR_OK;
}

static char *trim(char *s)
{
    char *e;
    while (*s == ' ' || *s == '\t')
        s++;
    e = s + strlen(s);
    while (e > s && (e[-1] == ' ' || e[-1] == '\t' || e[-1] == '\r' || e[-1] == '\n'))
        *--e = 0;
    return s;
}

static char *unquote(char *s)
{
    size_t n = strlen(s);
    if (n >= 2 && (s[0] == '\'' || s[0] == '"') && s[n - 1] == s[0]) {
        s[n - 1] = 0;
        return s + 1;
    }
    return s;
}

int ls_conf_load(const char *path, ls_conf_cb cb, void *ctx)
{
    char line[512];
    FILE *f = fopen(path, "r");
    if (!f)
        return -1;
    while (fgets(line, sizeof(line), f)) {
        char *s = trim(line);
        char *eq;
        if (!*s || *s == '#')
            continue;
        if (!strncmp(s, "option ", 7) || !strncmp(s, "list ", 5)) {
            char *k = trim(s + (*s == 'o' ? 7 : 5));
            char *sp = k;
            while (*sp && *sp != ' ' && *sp != '\t')
                sp++;
            if (!*sp)
                continue;
            *sp++ = 0;
            cb(unquote(k), unquote(trim(sp)), ctx);
            continue;
        }
        if (!strncmp(s, "config ", 7))
            continue;
        eq = strchr(s, '=');
        if (!eq)
            continue;
        *eq = 0;
        cb(trim(s), unquote(trim(eq + 1)), ctx);
    }
    fclose(f);
    return 0;
}

void ls_conf_env(const char *prefix, ls_conf_cb cb, void *ctx)
{
    size_t pl = strlen(prefix);
    char **e;
    for (e = environ; *e; e++) {
        char key[64];
        const char *eq;
        size_t i, n;
        if (strncmp(*e, prefix, pl) || (*e)[pl] != '_')
            continue;
        eq = strchr(*e, '=');
        if (!eq)
            continue;
        n = (size_t)(eq - *e) - pl - 1;
        if (n == 0 || n >= sizeof(key))
            continue;
        for (i = 0; i < n; i++) {
            char c = (*e)[pl + 1 + i];
            key[i] = (char)(c >= 'A' && c <= 'Z' ? c + 32 : c);
        }
        key[n] = 0;
        cb(key, eq + 1, ctx);
    }
}
