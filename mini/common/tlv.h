/* LSTP/1 framing: "LS" | version | type | length(u16 BE) | TLV payload.
 * TLV: tag(u8) | len(u16 BE) | value. */
#ifndef LS_TLV_H
#define LS_TLV_H

#include <stddef.h>
#include <stdint.h>

typedef struct {
    uint8_t *p;
    size_t cap;
    size_t len;
    size_t frame; /* offset of the current frame header */
    int err;
} ls_wr;

void ls_wr_init(ls_wr *w, void *buf, size_t cap);
void ls_frame_begin(ls_wr *w, uint8_t type);
/* Returns total frame length or 0 on overflow. */
size_t ls_frame_end(ls_wr *w);
void ls_put(ls_wr *w, uint8_t tag, const void *v, size_t len);
void ls_put_u8(ls_wr *w, uint8_t tag, uint8_t v);
void ls_put_u16(ls_wr *w, uint8_t tag, uint16_t v);
void ls_put_u32(ls_wr *w, uint8_t tag, uint32_t v);
void ls_put_u64(ls_wr *w, uint8_t tag, uint64_t v);
void ls_put_str(ls_wr *w, uint8_t tag, const char *s);
size_t ls_nest_begin(ls_wr *w, uint8_t tag);
void ls_nest_end(ls_wr *w, size_t off);

/* Parses a frame header at buf. Returns total frame length when a full frame
 * is present, 0 when more bytes are needed and -1 on a malformed header. */
long ls_frame_parse(const uint8_t *buf, size_t len, uint8_t *type,
                    const uint8_t **payload, size_t *plen);

typedef struct {
    const uint8_t *p;
    size_t len;
    size_t off;
} ls_rd;

void ls_rd_init(ls_rd *r, const void *p, size_t len);
/* Returns 1 and fills tag/value, 0 at end, -1 on malformed TLV. */
int ls_next(ls_rd *r, uint8_t *tag, const uint8_t **v, size_t *vlen);
/* Validates that the whole payload is well-formed TLV. */
int ls_tlv_valid(const uint8_t *p, size_t len);

/* Lookup helpers: return 1 when the tag is present with a correctly sized value. */
int ls_get(const uint8_t *p, size_t len, uint8_t tag, const uint8_t **v, size_t *vlen);
int ls_get_u8(const uint8_t *p, size_t len, uint8_t tag, uint8_t *out);
int ls_get_u16(const uint8_t *p, size_t len, uint8_t tag, uint16_t *out);
int ls_get_u32(const uint8_t *p, size_t len, uint8_t tag, uint32_t *out);
int ls_get_u64(const uint8_t *p, size_t len, uint8_t tag, uint64_t *out);
/* Copies a string value into out (NUL-terminated, truncated to cap-1). */
int ls_get_str(const uint8_t *p, size_t len, uint8_t tag, char *out, size_t cap);

uint16_t ls_be16(const uint8_t *p);
uint32_t ls_be32(const uint8_t *p);
uint64_t ls_be64(const uint8_t *p);
void ls_wbe16(uint8_t *p, uint16_t v);
void ls_wbe32(uint8_t *p, uint32_t v);
void ls_wbe64(uint8_t *p, uint64_t v);

#endif
