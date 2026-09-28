/* Minimal HTTP/1.1 request head parser (unit and property tested). */
#ifndef LSM_HTTPPARSE_H
#define LSM_HTTPPARSE_H

#include <stddef.h>

typedef struct {
    char method[8];
    char path[256];
    char auth[192];
    long content_length;
    int gzip;
} http_req;

/* Returns the length of the request head (including the blank line) when
 * complete, 0 when more data is needed and -1 when the request is invalid. */
long http_parse(const char *buf, size_t len, http_req *r);

#endif
