/* Portable parts of the interface inventory: globs and the parsers of Linux proc files
 * (unit tested on every host). The system calls live in netif_linux.c and netif_freebsd.c. */
#include "netif.h"
#include "lstp.h"
#include "util.h"

#include <ifaddrs.h>
#include <netinet/in.h>
#include <string.h>
#include <sys/socket.h>

/* Shell-style glob with '*' and '?' (no character classes). */
int ls_glob(const char *p, const char *s)
{
    const char *star = NULL, *ss = s;
    while (*s) {
        if (*p == '?' || *p == *s) {
            p++;
            s++;
        } else if (*p == '*') {
            star = p++;
            ss = s;
        } else if (star) {
            p = star + 1;
            s = ++ss;
        } else {
            return 0;
        }
    }
    while (*p == '*')
        p++;
    return *p == 0;
}

int ls_glob_list_match(const char *list, const char *name)
{
    char pat[64];
    while (list && *list) {
        const char *c = strchr(list, ',');
        size_t n = c ? (size_t)(c - list) : strlen(list);
        while (n && *list == ' ') {
            list++;
            n--;
        }
        while (n && list[n - 1] == ' ')
            n--;
        if (n && n < sizeof(pat)) {
            memcpy(pat, list, n);
            pat[n] = 0;
            if (ls_glob(pat, name))
                return 1;
        }
        list = c ? c + 1 : NULL;
    }
    return 0;
}

/* Walks lines; returns pointer to next line or NULL. Copies current line into l. */
static const char *next_line(const char *p, char *l, size_t cap)
{
    size_t n = 0;
    if (!p || !*p)
        return NULL;
    while (*p && *p != '\n') {
        if (n + 1 < cap)
            l[n++] = *p;
        p++;
    }
    l[n] = 0;
    return *p ? p + 1 : p;
}

/* Splits whitespace/'|' separated fields in place. */
static int fields(char *l, char **f, int max, const char *seps)
{
    int n = 0;
    char *s = l;
    while (*s && n < max) {
        while (*s && strchr(seps, *s))
            *s++ = 0;
        if (!*s)
            break;
        f[n++] = s;
        while (*s && !strchr(seps, *s))
            s++;
    }
    return n;
}

int ls_parse_vlan_config(const char *text, const char *dev, uint16_t *vid, char *parent, size_t pcap)
{
    char l[256];
    char *f[4];
    const char *p = text;
    while ((p = next_line(p, l, sizeof(l)))) {
        uint32_t v;
        if (fields(l, f, 4, " \t|") != 3)
            continue;
        if (strcmp(f[0], dev) || ls_parse_u32(f[1], &v) || v > 4094)
            continue;
        *vid = (uint16_t)v;
        if (parent)
            ls_strlcpy(parent, f[2], pcap);
        return 0;
    }
    return -1;
}

static int parse_u64(const char *s, uint64_t *out)
{
    uint64_t v = 0;
    if (!*s)
        return -1;
    for (; *s; s++) {
        if (*s < '0' || *s > '9')
            return -1;
        v = v * 10 + (uint64_t)(*s - '0');
    }
    *out = v;
    return 0;
}

int ls_parse_net_dev(const char *text, const char *dev, uint64_t *rx, uint64_t *tx)
{
    char l[512];
    char *f[17];
    const char *p = text;
    while ((p = next_line(p, l, sizeof(l)))) {
        char *colon = strchr(l, ':');
        char *name = l;
        if (!colon)
            continue;
        *colon = 0;
        while (*name == ' ')
            name++;
        if (strcmp(name, dev))
            continue;
        if (fields(colon + 1, f, 17, " \t") < 16)
            return -1;
        if (parse_u64(f[0], rx) || parse_u64(f[8], tx))
            return -1;
        return 0;
    }
    return -1;
}

static uint32_t hex32(const char *s, int *ok, int digits)
{
    uint32_t v = 0;
    int n = 0;
    for (; *s; s++, n++) {
        char c = *s;
        v <<= 4;
        if (c >= '0' && c <= '9')
            v |= (uint32_t)(c - '0');
        else if (c >= 'A' && c <= 'F')
            v |= (uint32_t)(c - 'A' + 10);
        else if (c >= 'a' && c <= 'f')
            v |= (uint32_t)(c - 'a' + 10);
        else
            *ok = 0;
    }
    if (n == 0 || n > 8 || (digits && n != digits))
        *ok = 0;
    return v;
}

int ls_parse_route(const char *text, uint32_t dst, char *dev, size_t cap)
{
    char l[256];
    char *f[11];
    const char *p = text;
    int best_len = -1;
    uint32_t best_metric = 0;
    /* /proc/net/route prints the in-memory (network order) value as a host
     * integer, so byte-swapping is not needed: compare raw values. */
    while ((p = next_line(p, l, sizeof(l)))) {
        int ok = 1, plen = 0;
        uint32_t d, m, metric, flags, bits;
        if (fields(l, f, 11, " \t") < 8 || !strcmp(f[0], "Iface"))
            continue;
        d = hex32(f[1], &ok, 8);
        flags = hex32(f[3], &ok, 0) & 0xffff;
        m = hex32(f[7], &ok, 8);
        if (!ok || ls_parse_u32(f[6], &metric) || !(flags & 1 /* RTF_UP */))
            continue;
        if ((dst & m) != d)
            continue;
        for (bits = m; bits; bits &= bits - 1)
            plen++;
        if (plen > best_len || (plen == best_len && metric < best_metric)) {
            best_len = plen;
            best_metric = metric;
            ls_strlcpy(dev, f[0], cap);
        }
    }
    return best_len < 0 ? -1 : 0;
}

int ls_parse_stat(const char *text, ls_cpu *c)
{
    char l[512];
    char *f[12];
    int n, i;
    uint64_t v[10];
    if (!next_line(text, l, sizeof(l)))
        return -1;
    n = fields(l, f, 12, " \t");
    if (n < 5 || strcmp(f[0], "cpu"))
        return -1;
    memset(v, 0, sizeof(v));
    for (i = 1; i < n && i <= 10; i++)
        if (parse_u64(f[i], &v[i - 1]))
            return -1;
    /* user nice system idle iowait irq softirq steal guest guest_nice;
     * guest time is already included in user/nice. */
    c->total = v[0] + v[1] + v[2] + v[3] + v[4] + v[5] + v[6] + v[7];
    c->busy = c->total - v[3] - v[4];
    return 0;
}

unsigned ls_cpu_permille(const ls_cpu *a, const ls_cpu *b)
{
    uint64_t t = b->total - a->total;
    uint64_t u = b->busy - a->busy;
    if (b->total <= a->total || b->busy < a->busy || !t)
        return 0;
    return (unsigned)(u * 1000 / t);
}

uint32_t ls_ethtool_speed(uint32_t raw)
{
    /* SPEED_UNKNOWN is -1; 0 and 65535 are reported by some drivers too */
    if (raw == 0 || raw == 0xffff || raw == 0xffffffffu)
        return 0;
    return raw;
}

int ls_dev_by_addr(uint32_t addr, char *dev, size_t cap)
{
    struct ifaddrs *ifa, *i;
    int rc = -1;
    if (getifaddrs(&ifa))
        return -1;
    for (i = ifa; i; i = i->ifa_next) {
        if (!i->ifa_addr || i->ifa_addr->sa_family != AF_INET)
            continue;
        if (((struct sockaddr_in *)(void *)i->ifa_addr)->sin_addr.s_addr == addr) {
            ls_strlcpy(dev, i->ifa_name, cap);
            rc = 0;
            break;
        }
    }
    freeifaddrs(ifa);
    return rc;
}
