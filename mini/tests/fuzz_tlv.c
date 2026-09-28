/* libFuzzer target for LSTP frame and TLV parsing. */
#include "../common/tlv.h"

#include <stddef.h>
#include <stdint.h>

int LLVMFuzzerTestOneInput(const uint8_t *data, size_t size);

int LLVMFuzzerTestOneInput(const uint8_t *data, size_t size)
{
    uint8_t type;
    const uint8_t *pl, *v;
    size_t plen, vl;
    char s[32];
    uint32_t u;
    if (ls_frame_parse(data, size, &type, &pl, &plen) > 0) {
        ls_get_str(pl, plen, 0x01, s, sizeof(s));
        ls_get_u32(pl, plen, 0x02, &u);
        if (ls_get(pl, plen, 0x40, &v, &vl))
            ls_tlv_valid(v, vl);
    }
    return 0;
}
