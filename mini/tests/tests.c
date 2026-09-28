/* Unit and property tests for the Mini common code. */
#include "../common/jw.h"
#include "../common/lstp.h"
#include "../common/netif.h"
#include "../common/sha256.h"
#include "../common/tlv.h"
#include "../common/util.h"
#include "../server/httpparse.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int failures, checks;

#define CHECK(c)                                                                 \
    do {                                                                         \
        checks++;                                                                \
        if (!(c)) {                                                              \
            failures++;                                                          \
            fprintf(stderr, "%s:%d: check failed: %s\n", __FILE__, __LINE__, #c); \
        }                                                                        \
    } while (0)

static void hex(const uint8_t *d, size_t n, char *out)
{
    size_t i;
    for (i = 0; i < n; i++)
        sprintf(out + 2 * i, "%02x", d[i]);
}

static void sha_vec(const char *msg, size_t len, const char *want)
{
    uint8_t d[32];
    char h[65];
    ls_sha256_once(msg, len, d);
    hex(d, 32, h);
    CHECK(!strcmp(h, want));
}

static void test_sha256(void)
{
    /* FIPS 180-4 / NIST CAVS examples */
    sha_vec("", 0, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
    sha_vec("abc", 3, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
    sha_vec("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq", 56,
            "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1");
    sha_vec("abcdefghbcdefghicdefghijdefghijkefghijklfghijklmghijklmnhijklmnoijklmnopjklmnopqklmnopqrlmnopqrsmnopqrst"
            "nopqrstu",
            112, "cf5b16a778af8380036ce59e7b0492370b249b11e8f07a51afac45037afee9d1");
    {
        /* one million 'a', fed in uneven chunks */
        static char a[1000];
        ls_sha256 c;
        uint8_t d[32];
        char h[65];
        int i;
        memset(a, 'a', sizeof(a));
        ls_sha256_init(&c);
        for (i = 0; i < 1000; i++) {
            ls_sha256_update(&c, a, 333);
            ls_sha256_update(&c, a, 667);
        }
        ls_sha256_final(&c, d);
        hex(d, 32, h);
        CHECK(!strcmp(h, "cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0"));
    }
}

static void hmac_vec(const uint8_t *key, size_t kl, const uint8_t *msg, size_t ml, const char *want)
{
    uint8_t d[32];
    char h[65];
    ls_hmac_once(key, kl, msg, ml, d);
    hex(d, 32, h);
    CHECK(!strncmp(h, want, strlen(want)));
}

static void test_hmac(void)
{
    uint8_t k[131];
    uint8_t m[50];
    /* RFC 4231 test cases 1-4, 5 (truncated), 6, 7 */
    memset(k, 0x0b, 20);
    hmac_vec(k, 20, (const uint8_t *)"Hi There", 8,
             "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7");
    hmac_vec((const uint8_t *)"Jefe", 4, (const uint8_t *)"what do ya want for nothing?", 28,
             "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843");
    memset(k, 0xaa, 20);
    memset(m, 0xdd, 50);
    hmac_vec(k, 20, m, 50, "773ea91e36800e46854db8ebd09181a72959098b3ef8c122d9635514ced565fe");
    {
        uint8_t k4[25];
        int i;
        for (i = 0; i < 25; i++)
            k4[i] = (uint8_t)(i + 1);
        memset(m, 0xcd, 50);
        hmac_vec(k4, 25, m, 50, "82558a389a443c0ea4cc819899f2083a85f0faa3e578f8077a2e3ff46729665b");
    }
    memset(k, 0x0c, 20);
    hmac_vec(k, 20, (const uint8_t *)"Test With Truncation", 20, "a3b6167473100ee06e0c796c2955552b");
    memset(k, 0xaa, 131);
    hmac_vec(k, 131, (const uint8_t *)"Test Using Larger Than Block-Size Key - Hash Key First", 54,
             "60e431591ee0b67f0d8a26aacbf5b77f8e0bc6213728c5140546040f0ee37f54");
    hmac_vec(k, 131,
             (const uint8_t *)"This is a test using a larger than block-size key and a larger than block-size data. "
                              "The key needs to be hashed before being used by the HMAC algorithm.",
             152, "9b09ffa71b942fcb27635fbcd5b0e944bfdc63644f0713938a7f51535c3a35e2");
    CHECK(ls_ct_equal("abc", "abc", 3));
    CHECK(!ls_ct_equal("abc", "abd", 3));
}

static void test_tlv(void)
{
    uint8_t buf[256];
    ls_wr w;
    size_t n, plen, off;
    uint8_t type, u8 = 0;
    uint16_t u16 = 0;
    uint32_t u32 = 0;
    uint64_t u64 = 0;
    const uint8_t *pl, *v;
    size_t vl;
    char s[16];
    long fl;

    ls_wr_init(&w, buf, sizeof(buf));
    ls_frame_begin(&w, LS_HELLO);
    ls_put_u8(&w, T_TEST_KIND, TK_TCP_THROUGHPUT);
    ls_put_u16(&w, T_UDP_PORT, 47700);
    ls_put_u32(&w, T_RUN_ID, 0xdeadbeef);
    ls_put_u64(&w, T_BYTES, 0x0102030405060708ull);
    ls_put_str(&w, T_AGENT_ID, "node-1");
    off = ls_nest_begin(&w, T_IFACE);
    ls_put_str(&w, T_IF_NAME, "eth0");
    ls_nest_end(&w, off);
    n = ls_frame_end(&w);
    CHECK(n > LSTP_HDR);
    CHECK(buf[0] == 'L' && buf[1] == 'S' && buf[2] == 1 && buf[3] == LS_HELLO);
    CHECK(ls_be16(buf + 4) == n - LSTP_HDR);
    /* partial frames need more data */
    CHECK(ls_frame_parse(buf, 3, &type, &pl, &plen) == 0);
    CHECK(ls_frame_parse(buf, n - 1, &type, &pl, &plen) == 0);
    fl = ls_frame_parse(buf, n, &type, &pl, &plen);
    CHECK(fl == (long)n && type == LS_HELLO);
    CHECK(ls_get_u8(pl, plen, T_TEST_KIND, &u8) && u8 == TK_TCP_THROUGHPUT);
    CHECK(ls_get_u16(pl, plen, T_UDP_PORT, &u16) && u16 == 47700);
    CHECK(ls_get_u32(pl, plen, T_RUN_ID, &u32) && u32 == 0xdeadbeef);
    CHECK(ls_get_u64(pl, plen, T_BYTES, &u64) && u64 == 0x0102030405060708ull);
    CHECK(ls_get_str(pl, plen, T_AGENT_ID, s, sizeof(s)) && !strcmp(s, "node-1"));
    CHECK(ls_get_str(pl, plen, T_AGENT_ID, s, 4) && !strcmp(s, "nod"));
    CHECK(!ls_get_u32(pl, plen, T_TEST_KIND, &u32)); /* wrong size */
    CHECK(!ls_get_u8(pl, plen, T_NONCE, &u8));       /* missing */
    CHECK(ls_get(pl, plen, T_IFACE, &v, &vl) && ls_get_str(v, vl, T_IF_NAME, s, sizeof(s)) && !strcmp(s, "eth0"));
    /* malformed: bad magic, bad version, truncated TLV */
    buf[0] = 'X';
    CHECK(ls_frame_parse(buf, n, &type, &pl, &plen) == -1);
    buf[0] = 'L';
    buf[2] = 2;
    CHECK(ls_frame_parse(buf, n, &type, &pl, &plen) == -1);
    buf[2] = 1;
    ls_wbe16(buf + LSTP_HDR + 1, 200); /* first TLV claims 200 bytes */
    CHECK(ls_frame_parse(buf, n, &type, &pl, &plen) == -1);
    /* overflow is reported */
    ls_wr_init(&w, buf, 10);
    ls_frame_begin(&w, LS_READY);
    ls_put_str(&w, T_AGENT_ID, "too long for the buffer");
    CHECK(ls_frame_end(&w) == 0);
}

static uint32_t rng = 12345;
static uint32_t rnd(void)
{
    rng ^= rng << 13;
    rng ^= rng >> 17;
    rng ^= rng << 5;
    return rng;
}

/* Property tests: random and mutated inputs never crash or read out of bounds
 * (run under UBSan; ASan/libFuzzer variants live in tests/fuzz_*.c). */
static void test_properties(void)
{
    static uint8_t b[512];
    int i;
    for (i = 0; i < 200000; i++) {
        size_t n = rnd() % sizeof(b), k;
        uint8_t type;
        const uint8_t *pl;
        size_t plen;
        long r;
        http_req hr;
        for (k = 0; k < n; k++)
            b[k] = (uint8_t)rnd();
        if (i & 1) {
            b[0] = 'L';
            if (n > 1)
                b[1] = 'S';
            if (n > 2)
                b[2] = 1;
            if (n > 5)
                ls_wbe16(b + 4, (uint16_t)(n - 6));
        }
        r = ls_frame_parse(b, n, &type, &pl, &plen);
        CHECK(r <= (long)n);
        if (r > 0) {
            ls_rd rd;
            uint8_t t;
            const uint8_t *v;
            size_t vl;
            CHECK(plen + LSTP_HDR == (size_t)r);
            ls_rd_init(&rd, pl, plen);
            while (ls_next(&rd, &t, &v, &vl) == 1)
                CHECK(v + vl <= pl + plen);
        }
        if (i % 3 == 0 && n > 20) {
            memcpy(b, "GET /x HTTP/1.1\r\n", 17);
            if (i % 2)
                memcpy(b + n - 4, "\r\n\r\n", 4);
        }
        r = http_parse((const char *)b, n, &hr);
        CHECK(r <= (long)n);
        CHECK(hr.content_length >= 0);
    }
}

static void test_http(void)
{
    http_req r;
    const char *ok = "POST /api/run?x=1 HTTP/1.1\r\nHost: a\r\nContent-Length: 12\r\nAuthorization: Basic YWJj\r\n"
                     "Accept-Encoding: br, GZIP\r\n\r\n{\"a\":1}";
    long n = http_parse(ok, strlen(ok), &r);
    CHECK(n == (long)(strstr(ok, "\r\n\r\n") - ok + 4));
    CHECK(!strcmp(r.method, "POST") && !strcmp(r.path, "/api/run?x=1"));
    CHECK(r.content_length == 12 && !strcmp(r.auth, "Basic YWJj") && r.gzip);
    CHECK(http_parse("GET / HTTP/1.1\r\n", 16, &r) == 0);
    CHECK(http_parse("GET / HTTP/1.1\r\n\r\n", 18, &r) == 18);
    CHECK(http_parse("GET x HTTP/1.1\r\n\r\n", 18, &r) == -1);
    CHECK(http_parse("GET / FTP/1.1\r\n\r\n", 17, &r) == -1);
    CHECK(http_parse("GET / HTTP/1.1\r\nContent-Length: -1\r\n\r\n", 38, &r) == -1);
    CHECK(http_parse("GET / HTTP/1.1\r\nBroken\r\n\r\n", 26, &r) == -1);
    CHECK(http_parse("VERYLONGMETHOD / HTTP/1.1\r\n\r\n", 29, &r) == -1);
}

static void test_parsers(void)
{
    char dev[16], par[16], txt[512];
    uint16_t vid = 0;
    uint64_t rx = 0, tx = 0;
    ls_cpu a, b;
    uint32_t net, dst, gw0 = 0;
    int prefix;
    const char *vlan = "VLAN Dev name    | VLAN ID\nName-Type: VLAN_NAME_TYPE_RAW_PLUS_VID_NO_PAD\n"
                       "eth1.300       | 300  | eth1\nlan2.100       | 100  | lan2\n";
    const char *netdev = "Inter-|   Receive                                                |  Transmit\n"
                         " face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop "
                         "fifo colls carrier compressed\n"
                         "    lo:    1000      10    0    0    0     0          0         0     1000      10    0    0    "
                         "0     0       0          0\n"
                         "  eth0: 123456789  1000    0    0    0     0          0         0 987654321   2000    0    0    "
                         "0     0       0          0\n";
    CHECK(ls_parse_vlan_config(vlan, "eth1.300", &vid, par, sizeof(par)) == 0 && vid == 300 && !strcmp(par, "eth1"));
    CHECK(ls_parse_vlan_config(vlan, "lan2.100", &vid, NULL, 0) == 0 && vid == 100);
    CHECK(ls_parse_vlan_config(vlan, "eth0", &vid, NULL, 0) == -1);
    CHECK(ls_parse_net_dev(netdev, "eth0", &rx, &tx) == 0 && rx == 123456789u && tx == 987654321u);
    CHECK(ls_parse_net_dev(netdev, "lo", &rx, &tx) == 0 && rx == 1000);
    CHECK(ls_parse_net_dev(netdev, "wan", &rx, &tx) == -1);
    CHECK(ls_parse_stat("cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 2 3 4\n", &a) == 0);
    CHECK(a.total == 1000 && a.busy == 200);
    CHECK(ls_parse_stat("cpu  400 0 400 1000 200 0 0 0 0 0\n", &b) == 0);
    CHECK(ls_cpu_permille(&a, &b) == 600);
    CHECK(ls_cpu_permille(&b, &a) == 0);
    CHECK(ls_parse_stat("intr 1 2 3\n", &a) == -1);
    CHECK(ls_ethtool_speed(0xffffffffu) == 0 && ls_ethtool_speed(2500) == 2500 && ls_ethtool_speed(0) == 0);
    /* /proc/net/route prints the raw in-memory address as a host integer */
    {
        uint32_t n1, m1, n2, m2, def = 0;
        ls_parse_cidr("10.10.1.0/24", &n1, &prefix);
        m1 = ls_prefix_mask(24);
        ls_parse_cidr("10.0.0.0/8", &n2, &prefix);
        m2 = ls_prefix_mask(8);
        snprintf(txt, sizeof(txt),
                 "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"
                 "eth9\t%08X\t%08X\t0003\t0\t0\t0\t%08X\t0\t0\t0\n"
                 "eth1\t%08X\t%08X\t0001\t0\t0\t0\t%08X\t0\t0\t0\n"
                 "eth2\t%08X\t%08X\t0001\t0\t0\t0\t%08X\t0\t0\t0\n",
                 def, gw0, def, n1, gw0, m1, n2, gw0, m2);
        ls_parse_ipv4("10.10.1.7", &dst);
        CHECK(ls_parse_route(txt, dst, dev, sizeof(dev)) == 0 && !strcmp(dev, "eth1"));
        ls_parse_ipv4("10.20.0.1", &dst);
        CHECK(ls_parse_route(txt, dst, dev, sizeof(dev)) == 0 && !strcmp(dev, "eth2"));
        ls_parse_ipv4("8.8.8.8", &dst);
        CHECK(ls_parse_route(txt, dst, dev, sizeof(dev)) == 0 && !strcmp(dev, "eth9"));
    }
    CHECK(ls_parse_cidr("10.31.0.77/24", &net, &prefix) == 0 && prefix == 24);
    ls_fmt(txt, sizeof(txt), "%I", net);
    CHECK(!strcmp(txt, "10.31.0.0"));
    CHECK(ls_parse_cidr("10.31.0.0/33", &net, &prefix) == -1);
    CHECK(ls_parse_ipv4("256.1.1.1", &net) == -1 && ls_parse_ipv4("1.2.3", &net) == -1);
    CHECK(ls_glob_list_match("wan*, tailscale*", "wan6") && ls_glob_list_match("wan*,tailscale*", "tailscale0"));
    CHECK(!ls_glob_list_match("wan*,tailscale*", "lan") && ls_glob("e?h*", "eth1.300") && !ls_glob("eth", "eth0"));
}

static void test_fmt_jw(void)
{
    char b[64];
    jw w;
    char *s;
    size_t n;
    ls_fmt(b, sizeof(b), "%s=%d/%u/%D/%U/%x/%h%%", "k", -5, 7u, (int64_t)-9, (uint64_t)18446744073709551615ull, 255u, 10u);
    CHECK(!strcmp(b, "k=-5/7/-9/18446744073709551615/ff/0a%"));
    CHECK(ls_fmt(b, 4, "abcdef") == 3 && !strcmp(b, "abc"));
    jw_init(&w);
    jw_obj(&w);
    jw_kstr(&w, "a", "q\"\\\n");
    jw_key(&w, "b");
    jw_arr(&w);
    jw_int(&w, -1);
    jw_uint(&w, 2);
    jw_bool(&w, 1);
    jw_null(&w);
    jw_obj(&w);
    jw_end_obj(&w);
    jw_end_arr(&w);
    jw_end_obj(&w);
    s = jw_take(&w, &n);
    CHECK(s && !strcmp(s, "{\"a\":\"q\\\"\\\\\\u000a\",\"b\":[-1,2,true,null,{}]}"));
    free(s);
}

int main(void)
{
    test_sha256();
    test_hmac();
    test_tlv();
    test_http();
    test_parsers();
    test_fmt_jw();
    test_properties();
    printf("%d checks, %d failures\n", checks, failures);
    return failures != 0;
}
