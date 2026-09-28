/* libFuzzer target for the HTTP request head parser. */
#include "../server/httpparse.h"

#include <stddef.h>
#include <stdint.h>

int LLVMFuzzerTestOneInput(const uint8_t *data, size_t size);

int LLVMFuzzerTestOneInput(const uint8_t *data, size_t size)
{
    http_req r;
    http_parse((const char *)data, size, &r);
    return 0;
}
