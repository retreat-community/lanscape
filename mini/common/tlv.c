#include "tlv.h"
#include "lstp.h"

#include <string.h>

uint16_t ls_be16(const uint8_t *p) { return (uint16_t)(p[0] << 8 | p[1]); }
uint32_t ls_be32(const uint8_t *p)
{
    return (uint32_t)p[0] << 24 | (uint32_t)p[1] << 16 | (uint32_t)p[2] << 8 | p[3];
}
uint64_t ls_be64(const uint8_t *p) { return (uint64_t)ls_be32(p) << 32 | ls_be32(p + 4); }
void ls_wbe16(uint8_t *p, uint16_t v) { p[0] = (uint8_t)(v >> 8); p[1] = (uint8_t)v; }
void ls_wbe32(uint8_t *p, uint32_t v)
{
    p[0] = (uint8_t)(v >> 24); p[1] = (uint8_t)(v >> 16);
    p[2] = (uint8_t)(v >> 8); p[3] = (uint8_t)v;
}
void ls_wbe64(uint8_t *p, uint64_t v) { ls_wbe32(p, (uint32_t)(v >> 32)); ls_wbe32(p + 4, (uint32_t)v); }

void ls_wr_init(ls_wr *w, void *buf, size_t cap)
{
    w->p = buf;
    w->cap = cap;
    w->len = 0;
    w->frame = 0;
    w->err = 0;
}

static uint8_t *reserve(ls_wr *w, size_t n)
{
    uint8_t *r;
    if (w->err || n > w->cap - w->len) {
        w->err = 1;
        return NULL;
    }
    r = w->p + w->len;
    w->len += n;
    return r;
}

void ls_frame_begin(ls_wr *w, uint8_t type)
{
    uint8_t *h;
    w->frame = w->len;
    h = reserve(w, LSTP_HDR);
    if (!h)
        return;
    h[0] = 'L';
    h[1] = 'S';
    h[2] = LSTP_VERSION;
    h[3] = type;
    h[4] = 0;
    h[5] = 0;
}

size_t ls_frame_end(ls_wr *w)
{
    size_t plen;
    if (w->err)
        return 0;
    plen = w->len - w->frame - LSTP_HDR;
    if (plen > LSTP_MAX_PAYLOAD) {
        w->err = 1;
        return 0;
    }
    ls_wbe16(w->p + w->frame + 4, (uint16_t)plen);
    return w->len - w->frame;
}

void ls_put(ls_wr *w, uint8_t tag, const void *v, size_t len)
{
    uint8_t *d;
    if (len > 0xffff) {
        w->err = 1;
        return;
    }
    d = reserve(w, 3 + len);
    if (!d)
        return;
    d[0] = tag;
    ls_wbe16(d + 1, (uint16_t)len);
    if (len)
        memcpy(d + 3, v, len);
}

void ls_put_u8(ls_wr *w, uint8_t tag, uint8_t v) { ls_put(w, tag, &v, 1); }
void ls_put_u16(ls_wr *w, uint8_t tag, uint16_t v)
{
    uint8_t b[2];
    ls_wbe16(b, v);
    ls_put(w, tag, b, 2);
}
void ls_put_u32(ls_wr *w, uint8_t tag, uint32_t v)
{
    uint8_t b[4];
    ls_wbe32(b, v);
    ls_put(w, tag, b, 4);
}
void ls_put_u64(ls_wr *w, uint8_t tag, uint64_t v)
{
    uint8_t b[8];
    ls_wbe64(b, v);
    ls_put(w, tag, b, 8);
}
void ls_put_str(ls_wr *w, uint8_t tag, const char *s) { ls_put(w, tag, s, strlen(s)); }

size_t ls_nest_begin(ls_wr *w, uint8_t tag)
{
    size_t off = w->len;
    uint8_t *d = reserve(w, 3);
    if (d) {
        d[0] = tag;
        d[1] = 0;
        d[2] = 0;
    }
    return off;
}

void ls_nest_end(ls_wr *w, size_t off)
{
    size_t n;
    if (w->err)
        return;
    n = w->len - off - 3;
    if (n > 0xffff) {
        w->err = 1;
        return;
    }
    ls_wbe16(w->p + off + 1, (uint16_t)n);
}

long ls_frame_parse(const uint8_t *buf, size_t len, uint8_t *type,
                    const uint8_t **payload, size_t *plen)
{
    size_t n;
    if (len >= 1 && buf[0] != 'L')
        return -1;
    if (len >= 2 && buf[1] != 'S')
        return -1;
    if (len >= 3 && buf[2] != LSTP_VERSION)
        return -1;
    if (len < LSTP_HDR)
        return 0;
    n = ls_be16(buf + 4);
    if (len < LSTP_HDR + n)
        return 0;
    if (!ls_tlv_valid(buf + LSTP_HDR, n))
        return -1;
    *type = buf[3];
    *payload = buf + LSTP_HDR;
    *plen = n;
    return (long)(LSTP_HDR + n);
}

void ls_rd_init(ls_rd *r, const void *p, size_t len)
{
    r->p = p;
    r->len = len;
    r->off = 0;
}

int ls_next(ls_rd *r, uint8_t *tag, const uint8_t **v, size_t *vlen)
{
    size_t n;
    if (r->off == r->len)
        return 0;
    if (r->len - r->off < 3)
        return -1;
    n = ls_be16(r->p + r->off + 1);
    if (n > r->len - r->off - 3)
        return -1;
    *tag = r->p[r->off];
    *v = r->p + r->off + 3;
    *vlen = n;
    r->off += 3 + n;
    return 1;
}

int ls_tlv_valid(const uint8_t *p, size_t len)
{
    ls_rd r;
    uint8_t t;
    const uint8_t *v;
    size_t vl;
    int rc;
    ls_rd_init(&r, p, len);
    while ((rc = ls_next(&r, &t, &v, &vl)) == 1)
        ;
    return rc == 0;
}

int ls_get(const uint8_t *p, size_t len, uint8_t tag, const uint8_t **v, size_t *vlen)
{
    ls_rd r;
    uint8_t t;
    ls_rd_init(&r, p, len);
    while (ls_next(&r, &t, v, vlen) == 1)
        if (t == tag)
            return 1;
    return 0;
}

int ls_get_u8(const uint8_t *p, size_t len, uint8_t tag, uint8_t *out)
{
    const uint8_t *v;
    size_t n;
    if (!ls_get(p, len, tag, &v, &n) || n != 1)
        return 0;
    *out = v[0];
    return 1;
}

int ls_get_u16(const uint8_t *p, size_t len, uint8_t tag, uint16_t *out)
{
    const uint8_t *v;
    size_t n;
    if (!ls_get(p, len, tag, &v, &n) || n != 2)
        return 0;
    *out = ls_be16(v);
    return 1;
}

int ls_get_u32(const uint8_t *p, size_t len, uint8_t tag, uint32_t *out)
{
    const uint8_t *v;
    size_t n;
    if (!ls_get(p, len, tag, &v, &n) || n != 4)
        return 0;
    *out = ls_be32(v);
    return 1;
}

int ls_get_u64(const uint8_t *p, size_t len, uint8_t tag, uint64_t *out)
{
    const uint8_t *v;
    size_t n;
    if (!ls_get(p, len, tag, &v, &n) || n != 8)
        return 0;
    *out = ls_be64(v);
    return 1;
}

int ls_get_str(const uint8_t *p, size_t len, uint8_t tag, char *out, size_t cap)
{
    const uint8_t *v;
    size_t n;
    if (cap == 0 || !ls_get(p, len, tag, &v, &n))
        return 0;
    if (n >= cap)
        n = cap - 1;
    memcpy(out, v, n);
    out[n] = 0;
    /* strings never carry embedded NULs */
    return memchr(out, 0, n) == NULL;
}
