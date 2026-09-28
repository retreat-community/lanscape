#include "httpparse.h"

#include <string.h>

static int ieq(const char *a, const char *b, size_t n)
{
    size_t i;
    for (i = 0; i < n; i++) {
        char x = a[i], y = b[i];
        if (x >= 'A' && x <= 'Z')
            x = (char)(x + 32);
        if (y >= 'A' && y <= 'Z')
            y = (char)(y + 32);
        if (x != y)
            return 0;
    }
    return 1;
}

static void copy(char *dst, size_t cap, const char *s, size_t n)
{
    if (n >= cap)
        n = cap - 1;
    memcpy(dst, s, n);
    dst[n] = 0;
}

long http_parse(const char *buf, size_t len, http_req *r)
{
    const char *end = NULL, *p, *line, *sp1, *sp2;
    size_t i;
    memset(r, 0, sizeof(*r));
    for (i = 3; i < len; i++)
        if (buf[i - 3] == '\r' && buf[i - 2] == '\n' && buf[i - 1] == '\r' && buf[i] == '\n') {
            end = buf + i + 1;
            break;
        }
    if (!end)
        return len > 8192 ? -1 : 0;
    /* request line */
    line = buf;
    p = memchr(line, '\r', (size_t)(end - line));
    if (!p)
        return -1;
    sp1 = memchr(line, ' ', (size_t)(p - line));
    if (!sp1 || sp1 == line || (size_t)(sp1 - line) >= sizeof(r->method))
        return -1;
    sp2 = memchr(sp1 + 1, ' ', (size_t)(p - sp1 - 1));
    if (!sp2 || sp2 == sp1 + 1 || sp1[1] != '/' || (size_t)(sp2 - sp1 - 1) >= sizeof(r->path))
        return -1;
    if ((size_t)(p - sp2 - 1) < 8 || memcmp(sp2 + 1, "HTTP/1.", 7))
        return -1;
    copy(r->method, sizeof(r->method), line, (size_t)(sp1 - line));
    copy(r->path, sizeof(r->path), sp1 + 1, (size_t)(sp2 - sp1 - 1));
    for (i = 0; r->path[i]; i++)
        if ((unsigned char)r->path[i] < 0x21)
            return -1;
    /* headers */
    line = p + 2;
    while (line < end - 2) {
        const char *colon, *v;
        size_t vl;
        p = memchr(line, '\r', (size_t)(end - line));
        if (!p || p[1] != '\n')
            return -1;
        colon = memchr(line, ':', (size_t)(p - line));
        if (!colon)
            return -1;
        v = colon + 1;
        while (v < p && (*v == ' ' || *v == '\t'))
            v++;
        vl = (size_t)(p - v);
        if ((size_t)(colon - line) == 14 && ieq(line, "content-length", 14)) {
            long n = 0;
            size_t k;
            if (!vl || vl > 9)
                return -1;
            for (k = 0; k < vl; k++) {
                if (v[k] < '0' || v[k] > '9')
                    return -1;
                n = n * 10 + (v[k] - '0');
            }
            r->content_length = n;
        } else if ((size_t)(colon - line) == 13 && ieq(line, "authorization", 13)) {
            copy(r->auth, sizeof(r->auth), v, vl);
        } else if ((size_t)(colon - line) == 15 && ieq(line, "accept-encoding", 15)) {
            size_t k;
            for (k = 0; k + 4 <= vl; k++)
                if (ieq(v + k, "gzip", 4))
                    r->gzip = 1;
        }
        line = p + 2;
    }
    return (long)(end - buf);
}
