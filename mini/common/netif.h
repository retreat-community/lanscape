/* Linux interface inventory, counters, routes and CPU sampling for lsm-agent. */
#ifndef LS_NETIF_H
#define LS_NETIF_H

#include <stddef.h>
#include <stdint.h>

#define LS_IFNAME 16
#define LS_MAX_IFS 32

typedef struct {
    char name[LS_IFNAME];
    uint8_t mac[6];
    uint32_t addr;   /* network order */
    int prefix;
    uint16_t vlan;   /* 0 when untagged or unknown */
    uint32_t speed;  /* Mbit/s, 0 when unknown */
    uint32_t mtu;
    uint32_t flags;  /* IFF_LS_* */
    char kind[16];   /* netlink link kind: vlan, macvlan, bridge, veth... empty for physical */
    char parent[LS_IFNAME];
} ls_if;

typedef struct {
    uint64_t busy;
    uint64_t total;
} ls_cpu;

int ls_glob(const char *pattern, const char *s);
/* Comma-separated glob list match (e.g. "wan*,tailscale*"). */
int ls_glob_list_match(const char *list, const char *name);

int ls_if_list(ls_if *out, int max, const char *exclude);
int ls_if_counters(const char *dev, uint64_t *rx, uint64_t *tx);
int ls_route_dev(uint32_t dst, char *dev, size_t cap);
int ls_dev_by_addr(uint32_t addr, char *dev, size_t cap);
int ls_ifindex(const char *dev);
int ls_cpu_read(ls_cpu *c);
unsigned ls_cpu_permille(const ls_cpu *a, const ls_cpu *b);

/* Pure parsers (unit tested). */
int ls_parse_vlan_config(const char *text, const char *dev, uint16_t *vid, char *parent, size_t pcap);
int ls_parse_net_dev(const char *text, const char *dev, uint64_t *rx, uint64_t *tx);
int ls_parse_route(const char *text, uint32_t dst, char *dev, size_t cap);
int ls_parse_stat(const char *text, ls_cpu *c);
uint32_t ls_ethtool_speed(uint32_t raw);

#endif
