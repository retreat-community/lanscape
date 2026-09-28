#include "netif.h"
#include "lstp.h"
#include "util.h"

#include <fcntl.h>
#include <ifaddrs.h>
#include <linux/ethtool.h>
#include <linux/if_link.h>
#include <linux/if_packet.h>
#include <linux/netlink.h>
#include <linux/rtnetlink.h>
#include <linux/sockios.h>
#include <net/if.h>
#include <netinet/in.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/socket.h>
#include <unistd.h>

static char filebuf[16384];

static const char *read_file(const char *path)
{
    int fd = open(path, O_RDONLY | O_CLOEXEC);
    size_t n = 0;
    if (fd < 0)
        return NULL;
    while (n < sizeof(filebuf) - 1) {
        ssize_t r = read(fd, filebuf + n, sizeof(filebuf) - 1 - n);
        if (r <= 0)
            break;
        n += (size_t)r;
    }
    close(fd);
    filebuf[n] = 0;
    return filebuf;
}

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

int ls_cpu_read(ls_cpu *c)
{
    const char *t = read_file("/proc/stat");
    return t ? ls_parse_stat(t, c) : -1;
}

int ls_if_counters(const char *dev, uint64_t *rx, uint64_t *tx)
{
    const char *t = read_file("/proc/net/dev");
    return t ? ls_parse_net_dev(t, dev, rx, tx) : -1;
}

int ls_route_dev(uint32_t dst, char *dev, size_t cap)
{
    const char *t = read_file("/proc/net/route");
    return t ? ls_parse_route(t, dst, dev, cap) : -1;
}

int ls_ifindex(const char *dev)
{
    struct ifreq r;
    int s, rc;
    memset(&r, 0, sizeof(r));
    ls_strlcpy(r.ifr_name, dev, sizeof(r.ifr_name));
    s = socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0);
    if (s < 0)
        return -1;
    rc = ioctl(s, SIOCGIFINDEX, &r);
    close(s);
    return rc < 0 ? -1 : r.ifr_ifindex;
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

static uint32_t link_speed(int s, const char *dev)
{
    struct ifreq r;
    struct ethtool_cmd e;
    memset(&r, 0, sizeof(r));
    memset(&e, 0, sizeof(e));
    ls_strlcpy(r.ifr_name, dev, sizeof(r.ifr_name));
    e.cmd = ETHTOOL_GSET;
    r.ifr_data = (char *)&e;
    if (ioctl(s, SIOCETHTOOL, &r) < 0)
        return 0;
    return ls_ethtool_speed((uint32_t)e.speed | (uint32_t)e.speed_hi << 16);
}

/* Queries link kind and parent via RTM_GETLINK. */
static void link_info(int ifindex, char *kind, size_t kcap, char *parent, size_t pcap)
{
    struct {
        struct nlmsghdr nh;
        struct ifinfomsg ifi;
    } req;
    static uint8_t buf[8192];
    struct nlmsghdr *nh;
    ssize_t n;
    int s = socket(AF_NETLINK, SOCK_RAW | SOCK_CLOEXEC, NETLINK_ROUTE);
    kind[0] = 0;
    parent[0] = 0;
    if (s < 0)
        return;
    memset(&req, 0, sizeof(req));
    req.nh.nlmsg_len = sizeof(req);
    req.nh.nlmsg_type = RTM_GETLINK;
    req.nh.nlmsg_flags = NLM_F_REQUEST;
    req.ifi.ifi_family = AF_UNSPEC;
    req.ifi.ifi_index = ifindex;
    if (send(s, &req, sizeof(req), 0) < 0) {
        close(s);
        return;
    }
    n = recv(s, buf, sizeof(buf), 0);
    close(s);
    if (n <= 0)
        return;
    for (nh = (struct nlmsghdr *)(void *)buf; NLMSG_OK(nh, (unsigned)n); nh = NLMSG_NEXT(nh, n)) {
        struct ifinfomsg *ifi;
        struct rtattr *a;
        int len;
        if (nh->nlmsg_type != RTM_NEWLINK)
            continue;
        ifi = NLMSG_DATA(nh);
        len = (int)IFLA_PAYLOAD(nh);
        for (a = IFLA_RTA(ifi); RTA_OK(a, len); a = RTA_NEXT(a, len)) {
            if (a->rta_type == IFLA_LINK && RTA_PAYLOAD(a) >= 4) {
                int pidx;
                char pn[IF_NAMESIZE];
                memcpy(&pidx, RTA_DATA(a), 4);
                if (pidx != ifindex && if_indextoname((unsigned)pidx, pn))
                    ls_strlcpy(parent, pn, pcap);
            } else if (a->rta_type == IFLA_LINKINFO) {
                struct rtattr *b = RTA_DATA(a);
                int bl = (int)RTA_PAYLOAD(a);
                for (; RTA_OK(b, bl); b = RTA_NEXT(b, bl)) {
                    if (b->rta_type == IFLA_INFO_KIND) {
                        size_t kl = RTA_PAYLOAD(b);
                        const char *k = RTA_DATA(b);
                        while (kl && k[kl - 1] == 0)
                            kl--;
                        if (kl >= kcap)
                            kl = kcap - 1;
                        memcpy(kind, k, kl);
                        kind[kl] = 0;
                    }
                }
            }
        }
    }
}

int ls_if_list(ls_if *out, int max, const char *exclude)
{
    struct ifaddrs *ifa, *i, *j;
    const char *vlancfg;
    int n = 0, s;
    if (getifaddrs(&ifa))
        return -1;
    s = socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0);
    vlancfg = NULL;
    for (i = ifa; i && n < max; i = i->ifa_next) {
        ls_if *o;
        struct ifreq r;
        if (!i->ifa_addr || i->ifa_addr->sa_family != AF_INET || !i->ifa_netmask)
            continue;
        if ((i->ifa_flags & IFF_LOOPBACK) || ls_glob_list_match(exclude, i->ifa_name))
            continue;
        o = &out[n];
        memset(o, 0, sizeof(*o));
        ls_strlcpy(o->name, i->ifa_name, sizeof(o->name));
        o->addr = ((struct sockaddr_in *)(void *)i->ifa_addr)->sin_addr.s_addr;
        {
            uint32_t m = ((struct sockaddr_in *)(void *)i->ifa_netmask)->sin_addr.s_addr;
            for (; m; m &= m - 1)
                o->prefix++;
        }
        if (i->ifa_flags & IFF_UP)
            o->flags |= IFF_LS_UP;
        if (i->ifa_flags & IFF_RUNNING)
            o->flags |= IFF_LS_CARRIER;
        for (j = ifa; j; j = j->ifa_next) {
            if (j->ifa_addr && j->ifa_addr->sa_family == AF_PACKET && !strcmp(j->ifa_name, i->ifa_name)) {
                struct sockaddr_ll *ll = (struct sockaddr_ll *)(void *)j->ifa_addr;
                if (ll->sll_halen == 6)
                    memcpy(o->mac, ll->sll_addr, 6);
            }
        }
        if (s >= 0) {
            memset(&r, 0, sizeof(r));
            ls_strlcpy(r.ifr_name, o->name, sizeof(r.ifr_name));
            if (ioctl(s, SIOCGIFMTU, &r) == 0)
                o->mtu = (uint32_t)r.ifr_mtu;
            o->speed = link_speed(s, o->name);
        }
        link_info((int)if_nametoindex(o->name), o->kind, sizeof(o->kind), o->parent, sizeof(o->parent));
        if (!vlancfg)
            vlancfg = read_file("/proc/net/vlan/config");
        if (vlancfg) {
            char par[LS_IFNAME];
            /* read_file reuses one buffer; re-read after other reads */
            if (ls_parse_vlan_config(vlancfg, o->name, &o->vlan, par, sizeof(par)) == 0 && !o->parent[0])
                ls_strlcpy(o->parent, par, sizeof(o->parent));
        }
        n++;
    }
    if (s >= 0)
        close(s);
    freeifaddrs(ifa);
    return n;
}
