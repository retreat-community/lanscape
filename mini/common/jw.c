#include "jw.h"
#include "util.h"

#include <stdlib.h>
#include <string.h>

void jw_init(jw *w)
{
    memset(w, 0, sizeof(*w));
}

void jw_free(jw *w)
{
    free(w->buf);
    w->buf = NULL;
    w->len = w->cap = 0;
}

char *jw_take(jw *w, size_t *len)
{
    char *b;
    if (w->err || !w->buf) {
        jw_free(w);
        return NULL;
    }
    b = w->buf;
    if (len)
        *len = w->len;
    w->buf = NULL;
    w->len = w->cap = 0;
    return b;
}

static void put(jw *w, const char *s, size_t n)
{
    if (w->err)
        return;
    if (w->len + n + 1 > w->cap) {
        size_t nc = w->cap ? w->cap : 1024;
        char *nb;
        while (nc < w->len + n + 1)
            nc *= 2;
        nb = realloc(w->buf, nc);
        if (!nb) {
            w->err = 1;
            return;
        }
        w->buf = nb;
        w->cap = nc;
    }
    memcpy(w->buf + w->len, s, n);
    w->len += n;
    w->buf[w->len] = 0;
}

static void pre(jw *w)
{
    if (w->after_key) {
        w->after_key = 0;
        return;
    }
    if (w->depth > 0 && w->need_comma[w->depth - 1])
        put(w, ",", 1);
    if (w->depth > 0)
        w->need_comma[w->depth - 1] = 1;
}

static void open_(jw *w, const char *c)
{
    pre(w);
    put(w, c, 1);
    if (w->depth >= JW_MAX_DEPTH) {
        w->err = 1;
        return;
    }
    w->need_comma[w->depth++] = 0;
}

static void close_(jw *w, const char *c)
{
    if (w->depth <= 0) {
        w->err = 1;
        return;
    }
    w->depth--;
    put(w, c, 1);
}

void jw_obj(jw *w) { open_(w, "{"); }
void jw_end_obj(jw *w) { close_(w, "}"); }
void jw_arr(jw *w) { open_(w, "["); }
void jw_end_arr(jw *w) { close_(w, "]"); }

static void esc(jw *w, const char *s, size_t n)
{
    size_t i;
    put(w, "\"", 1);
    for (i = 0; i < n; i++) {
        unsigned char c = (unsigned char)s[i];
        char t[8];
        if (c == '"' || c == '\\') {
            t[0] = '\\';
            t[1] = (char)c;
            put(w, t, 2);
        } else if (c < 0x20) {
            put(w, t, ls_fmt(t, sizeof(t), "\\u00%h", c));
        } else {
            put(w, (const char *)&s[i], 1);
        }
    }
    put(w, "\"", 1);
}

void jw_key(jw *w, const char *k)
{
    pre(w);
    esc(w, k, strlen(k));
    put(w, ":", 1);
    w->after_key = 1;
}

void jw_strn(jw *w, const char *s, size_t n)
{
    pre(w);
    esc(w, s, n);
}

void jw_str(jw *w, const char *s) { jw_strn(w, s, strlen(s)); }

void jw_int(jw *w, int64_t v)
{
    char t[24];
    pre(w);
    put(w, t, ls_fmt(t, sizeof(t), "%D", v));
}

void jw_uint(jw *w, uint64_t v)
{
    char t[24];
    pre(w);
    put(w, t, ls_fmt(t, sizeof(t), "%U", v));
}

void jw_bool(jw *w, int v)
{
    pre(w);
    if (v)
        put(w, "true", 4);
    else
        put(w, "false", 5);
}

void jw_null(jw *w)
{
    pre(w);
    put(w, "null", 4);
}

void jw_raw(jw *w, const char *s, size_t n)
{
    pre(w);
    put(w, s, n);
}

void jw_ip(jw *w, uint32_t addr_be)
{
    char t[20];
    ls_fmt(t, sizeof(t), "%I", addr_be);
    jw_str(w, t);
}

void jw_kstr(jw *w, const char *k, const char *v) { jw_key(w, k); jw_str(w, v); }
void jw_kint(jw *w, const char *k, int64_t v) { jw_key(w, k); jw_int(w, v); }
void jw_kuint(jw *w, const char *k, uint64_t v) { jw_key(w, k); jw_uint(w, v); }
void jw_kbool(jw *w, const char *k, int v) { jw_key(w, k); jw_bool(w, v); }
