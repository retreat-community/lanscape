/* FreeBSD interface inventory, counters, routes and CPU sampling (getifaddrs/if_data, routing
 * socket, SIOCGETVLAN, kern.cp_time). */
#include "netif.h"
#include "lstp.h"
#include "util.h"

#include <errno.h>
#include <ifaddrs.h>
#include <net/if.h>
#include <net/if_dl.h>
#include <net/if_types.h>
#include <net/if_vlan_var.h>
#include <net/route.h>
#include <netinet/in.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/resource.h>
#include <sys/socket.h>
#include <sys/sysctl.h>
#include <unistd.h>

int ls_cpu_read(ls_cpu *c)
{
    long t[CPUSTATES];
    size_t len = sizeof(t);
    int i;
    if (sysctlbyname("kern.cp_time", t, &len, NULL, 0) < 0 || len != sizeof(t))
        return -1;
    c->total = 0;
    for (i = 0; i < CPUSTATES; i++)
        c->total += (uint64_t)t[i];
    c->busy = c->total - (uint64_t)t[CP_IDLE];
    return 0;
}

static struct if_data *link_data(struct ifaddrs *ifa, const char *dev, struct sockaddr_dl **sdl)
{
    struct ifaddrs *i;
    for (i = ifa; i; i = i->ifa_next) {
        if (i->ifa_addr && i->ifa_addr->sa_family == AF_LINK && !strcmp(i->ifa_name, dev)) {
            if (sdl)
                *sdl = (struct sockaddr_dl *)(void *)i->ifa_addr;
            return i->ifa_data;
        }
    }
    return NULL;
}

int ls_if_counters(const char *dev, uint64_t *rx, uint64_t *tx)
{
    struct ifaddrs *ifa;
    struct if_data *d;
    int rc = -1;
    if (getifaddrs(&ifa))
        return -1;
    d = link_data(ifa, dev, NULL);
    if (d) {
        *rx = d->ifi_ibytes;
        *tx = d->ifi_obytes;
        rc = 0;
    }
    freeifaddrs(ifa);
    return rc;
}

/* Asks the routing socket which interface carries traffic to dst (RTM_GET). */
int ls_route_dev(uint32_t dst, char *dev, size_t cap)
{
    struct {
        struct rt_msghdr h;
        uint8_t space[512];
    } m;
    struct sockaddr_in *sin;
    struct sockaddr_dl *ifp;
    int s, seq = 1, i;
    ssize_t n;
    uint8_t *p;
    pid_t pid = getpid();
    memset(&m, 0, sizeof(m));
    m.h.rtm_msglen = sizeof(m.h) + 2 * sizeof(struct sockaddr_in);
    m.h.rtm_version = RTM_VERSION;
    m.h.rtm_type = RTM_GET;
    m.h.rtm_addrs = RTA_DST | RTA_IFP;
    m.h.rtm_seq = seq;
    sin = (struct sockaddr_in *)(void *)m.space;
    sin->sin_len = sizeof(*sin);
    sin->sin_family = AF_INET;
    sin->sin_addr.s_addr = dst;
    ifp = (struct sockaddr_dl *)(void *)(m.space + sizeof(*sin));
    ifp->sdl_len = sizeof(struct sockaddr_in); /* an empty AF_LINK address asks for the interface */
    ifp->sdl_family = AF_LINK;
    s = socket(PF_ROUTE, SOCK_RAW | SOCK_CLOEXEC, 0);
    if (s < 0)
        return -1;
    if (write(s, &m, m.h.rtm_msglen) < 0) {
        close(s);
        return -1;
    }
    do {
        n = read(s, &m, sizeof(m));
    } while (n > 0 && (m.h.rtm_seq != seq || m.h.rtm_pid != pid));
    close(s);
    if (n <= 0 || m.h.rtm_errno)
        return -1;
    p = m.space;
    for (i = 0; i < RTAX_MAX; i++) {
        struct sockaddr *sa = (struct sockaddr *)(void *)p;
        if (!(m.h.rtm_addrs & (1 << i)))
            continue;
        if (p >= (uint8_t *)&m + n)
            break;
        if (i == RTAX_IFP && sa->sa_family == AF_LINK) {
            struct sockaddr_dl *dl = (struct sockaddr_dl *)(void *)sa;
            size_t l = dl->sdl_nlen < cap ? dl->sdl_nlen : cap - 1;
            memcpy(dev, dl->sdl_data, l);
            dev[l] = 0;
            return l ? 0 : -1;
        }
        p += sa->sa_len ? ((sa->sa_len + sizeof(long) - 1) & ~(sizeof(long) - 1)) : sizeof(long);
    }
    return -1;
}

int ls_ifindex(const char *dev)
{
    unsigned i = if_nametoindex(dev);
    return i ? (int)i : -1;
}

static const char *kind_of(uint8_t type)
{
    switch (type) {
    case IFT_L2VLAN:
        return "vlan";
    case IFT_BRIDGE:
        return "bridge";
    case IFT_IEEE8023ADLAG:
        return "bond";
    case IFT_TUNNEL:
    case IFT_PROPVIRTUAL:
        return "tun";
    default:
        return "";
    }
}

int ls_if_list(ls_if *out, int max, const char *exclude)
{
    struct ifaddrs *ifa, *i;
    int n = 0, s;
    if (getifaddrs(&ifa))
        return -1;
    s = socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0);
    for (i = ifa; i && n < max; i = i->ifa_next) {
        ls_if *o;
        struct sockaddr_dl *sdl = NULL;
        struct if_data *d;
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
        d = link_data(ifa, o->name, &sdl);
        if (d) {
            o->mtu = d->ifi_mtu;
            /* ifi_baudrate is bit/s; 0 when the driver does not know */
            o->speed = (uint32_t)(d->ifi_baudrate / 1000000u);
            if (d->ifi_link_state == LINK_STATE_UP || (d->ifi_link_state == LINK_STATE_UNKNOWN && (i->ifa_flags & IFF_RUNNING)))
                o->flags |= IFF_LS_CARRIER;
            ls_strlcpy(o->kind, kind_of(d->ifi_type), sizeof(o->kind));
        }
        if (sdl && sdl->sdl_alen == 6)
            memcpy(o->mac, LLADDR(sdl), 6);
        if (s >= 0 && !strcmp(o->kind, "vlan")) {
            struct vlanreq vr;
            struct ifreq r;
            memset(&vr, 0, sizeof(vr));
            memset(&r, 0, sizeof(r));
            ls_strlcpy(r.ifr_name, o->name, sizeof(r.ifr_name));
            r.ifr_data = (caddr_t)&vr;
            if (ioctl(s, SIOCGETVLAN, &r) == 0) {
                o->vlan = (uint16_t)(vr.vlr_tag & 0x0fff);
                ls_strlcpy(o->parent, vr.vlr_parent, sizeof(o->parent));
            }
        }
        n++;
    }
    if (s >= 0)
        close(s);
    freeifaddrs(ifa);
    return n;
}

/* FreeBSD has no SO_BINDTODEVICE: the source address selects the interface, and the route
 * check before and the counter check after a test confirm the path. */
int ls_bind_dev(int s, const char *dev)
{
    (void)s;
    return if_nametoindex(dev) ? 0 : (errno = ENODEV, -1);
}

void ls_set_df(int s, int df)
{
    int v = df ? 1 : 0;
    setsockopt(s, IPPROTO_IP, IP_DONTFRAG, &v, sizeof(v));
}

int ls_outq(int s)
{
    int q = 0;
    return ioctl(s, FIONWRITE, &q) == 0 ? q : 0;
}
