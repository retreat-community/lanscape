/* Streaming JSON writer into a growable buffer. No parser by design. */
#ifndef LS_JW_H
#define LS_JW_H

#include <stddef.h>
#include <stdint.h>

#define JW_MAX_DEPTH 32

typedef struct {
    char *buf;
    size_t len;
    size_t cap;
    int err;
    int depth;
    uint8_t need_comma[JW_MAX_DEPTH];
    uint8_t after_key;
} jw;

void jw_init(jw *w);
void jw_free(jw *w);
/* Detaches the buffer; caller frees. NUL-terminated. */
char *jw_take(jw *w, size_t *len);
void jw_obj(jw *w);
void jw_end_obj(jw *w);
void jw_arr(jw *w);
void jw_end_arr(jw *w);
void jw_key(jw *w, const char *k);
void jw_str(jw *w, const char *s);
void jw_strn(jw *w, const char *s, size_t n);
void jw_int(jw *w, int64_t v);
void jw_uint(jw *w, uint64_t v);
void jw_bool(jw *w, int v);
void jw_null(jw *w);
/* Appends pre-serialised JSON as a value. */
void jw_raw(jw *w, const char *s, size_t n);
void jw_ip(jw *w, uint32_t addr_be);

/* Convenience key/value helpers. */
void jw_kstr(jw *w, const char *k, const char *v);
void jw_kint(jw *w, const char *k, int64_t v);
void jw_kuint(jw *w, const char *k, uint64_t v);
void jw_kbool(jw *w, const char *k, int v);

#endif
