/* SHA-256 (FIPS 180-4) and HMAC-SHA256 (RFC 2104). */
#ifndef LS_SHA256_H
#define LS_SHA256_H

#include <stddef.h>
#include <stdint.h>

#define LS_SHA256_LEN 32
#define LS_SHA256_BLOCK 64

typedef struct {
    uint32_t h[8];
    uint64_t total;
    uint8_t buf[LS_SHA256_BLOCK];
    size_t used;
} ls_sha256;

void ls_sha256_init(ls_sha256 *c);
void ls_sha256_update(ls_sha256 *c, const void *data, size_t len);
void ls_sha256_final(ls_sha256 *c, uint8_t out[LS_SHA256_LEN]);
void ls_sha256_once(const void *data, size_t len, uint8_t out[LS_SHA256_LEN]);

typedef struct {
    ls_sha256 inner;
    ls_sha256 outer;
} ls_hmac;

void ls_hmac_init(ls_hmac *c, const void *key, size_t klen);
void ls_hmac_update(ls_hmac *c, const void *data, size_t len);
void ls_hmac_final(ls_hmac *c, uint8_t out[LS_SHA256_LEN]);
void ls_hmac_once(const void *key, size_t klen, const void *data, size_t len,
                  uint8_t out[LS_SHA256_LEN]);

/* Constant-time comparison; returns 1 when equal. */
int ls_ct_equal(const void *a, const void *b, size_t len);

#endif
