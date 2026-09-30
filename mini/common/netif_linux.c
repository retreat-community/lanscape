/* Linux interface inventory, counters, routes and CPU sampling (netlink, ethtool, /proc). */
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

int ls_bind_dev(int s, const char *dev)
{
    return setsockopt(s, SOL_SOCKET, SO_BINDTODEVICE, dev, (socklen_t)strlen(dev));
}

void ls_set_df(int s, int df)
{
    int v = df ? IP_PMTUDISC_PROBE : IP_PMTUDISC_DONT;
    setsockopt(s, IPPROTO_IP, IP_MTU_DISCOVER, &v, sizeof(v));
}

int ls_outq(int s)
{
    int q = 0;
    return ioctl(s, SIOCOUTQ, &q) == 0 ? q : 0;
}
