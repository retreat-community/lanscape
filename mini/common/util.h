/* Small runtime helpers shared by lsm-agent and lsm-server. */
#ifndef LS_UTIL_H
#define LS_UTIL_H

#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>

#ifndef LS_VERSION
#define LS_VERSION "dev"
#endif

enum { LOG_E = 0, LOG_W = 1, LOG_I = 2, LOG_D = 3 };
extern int ls_log_level;

/* Minimal formatter without floating point support.
 * %s %d %u %x %c %%, %D int64, %U uint64, %I IPv4 address (uint32 in network order),
 * %h two-digit lowercase hex of an unsigned int. Always NUL-terminates. */
size_t ls_vfmt(char *buf, size_t cap, const char *fmt, va_list ap);
size_t ls_fmt(char *buf, size_t cap, const char *fmt, ...);
void ls_log(int level, const char *fmt, ...);

uint64_t ls_now_us(void);
uint64_t ls_wall_ms(void);
int ls_random(void *buf, size_t len);
void ls_strlcpy(char *dst, const char *src, size_t cap);
int ls_set_nonblock(int fd);
int ls_parse_ipv4(const char *s, uint32_t *out);
int ls_parse_cidr(const char *s, uint32_t *net, int *prefix);
uint32_t ls_prefix_mask(int prefix);
int ls_parse_u32(const char *s, uint32_t *out);

/* Blocking I/O with a deadline (monotonic us). */
enum { LSR_OK = 1, LSR_EOF = 0, LSR_TIMEOUT = -1, LSR_ERR = -2, LSR_PROTO = -3 };
int ls_write_all(int fd, const void *buf, size_t len, uint64_t deadline);
/* Reads exactly one LSTP frame into buf. */
int ls_read_frame(int fd, uint8_t *buf, size_t cap, uint64_t deadline, uint8_t *type,
                  const uint8_t **payload, size_t *plen);

/* Config: key=value lines or UCI "option key 'value'" / "list key 'value'". */
typedef void (*ls_conf_cb)(const char *key, const char *val, void *ctx);
int ls_conf_load(const char *path, ls_conf_cb cb, void *ctx);
/* Calls cb for every environment variable PREFIX_KEY (key lowercased). */
void ls_conf_env(const char *prefix, ls_conf_cb cb, void *ctx);

#endif
