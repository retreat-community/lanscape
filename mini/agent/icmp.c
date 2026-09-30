/* ICMP echo for reachability, RTT and PMTU (DF) probes, bound to a device. */
#include "agent.h"
#include "../common/netif.h"
#include "../common/util.h"

#include <errno.h>
#include <netinet/in.h>
#include <poll.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

#define MAX_PROBES 64

static uint16_t csum(const uint8_t *p, size_t n)
{
    uint32_t s = 0;
    size_t i;
    for (i = 0; i + 1 < n; i += 2)
        s += (uint32_t)(p[i] << 8 | p[i + 1]);
    if (n & 1)
        s += (uint32_t)p[n - 1] << 8;
    while (s >> 16)
        s = (s & 0xffff) + (s >> 16);
    return (uint16_t)~s;
}

static int open_icmp(const cmd_t *c, int df, int *raw)
{
    struct sockaddr_in sa;
    int s = socket(AF_INET, SOCK_RAW | SOCK_CLOEXEC, IPPROTO_ICMP);
    *raw = 1;
    if (s < 0) {
        s = socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, IPPROTO_ICMP);
        *raw = 0;
    }
    if (s < 0)
        return -1;
    if (c->dev[0] && ls_bind_dev(s, c->dev) < 0) {
        close(s);
        return -2;
    }
    ls_set_df(s, df);
    memset(&sa, 0, sizeof(sa));
    sa.sin_family = AF_INET;
    sa.sin_addr.s_addr = c->src;
    if (c->src && bind(s, (struct sockaddr *)&sa, sizeof(sa)) < 0) {
        close(s);
        return -2;
    }
    return s;
}

void icmp_run(const cmd_t *c, result_t *r, int df)
{
    static uint8_t pkt[9216];
    static uint8_t in[9216 + 64];
    uint64_t sent_at[MAX_PROBES];
    uint8_t got[MAX_PROBES];
    struct sockaddr_in to;
    uint16_t id;
    int raw, s, count = c->count ? c->count : 10;
    int size = c->size ? c->size : 84;
    int interval = c->interval_ms ? c->interval_ms : 100;
    int i, payload;
    uint64_t sum = 0, next, end;

    if (count > MAX_PROBES)
        count = MAX_PROBES;
    if (size < 28 + 16 || size > (int)sizeof(pkt))
        size = size < 44 ? 44 : (int)sizeof(pkt);
    payload = size - 20; /* ICMP header + data */
    s = open_icmp(c, df, &raw);
    if (s < 0) {
        r->status = s == -2 ? ST_NO_DEVICE : ST_INTERNAL;
        ls_fmt(r->msg, sizeof(r->msg), "icmp socket: %d", errno);
        return;
    }
    ls_random(&id, sizeof(id));
    memset(got, 0, sizeof(got));
    memset(&to, 0, sizeof(to));
    to.sin_family = AF_INET;
    to.sin_addr.s_addr = c->dst;
    r->rtt_min = 0xffffffffu;
    next = ls_now_us();
    end = 0;
    i = 0;
    for (;;) {
        uint64_t now = ls_now_us();
        struct pollfd pfd;
        int wait;
        if (i < count && now >= next) {
            memset(pkt, 0, (size_t)payload);
            pkt[0] = 8;
            pkt[4] = (uint8_t)(id >> 8);
            pkt[5] = (uint8_t)id;
            pkt[6] = (uint8_t)(i >> 8);
            pkt[7] = (uint8_t)i;
            {
                int k;
                for (k = 8; k < payload; k++)
                    pkt[k] = (uint8_t)k;
            }
            {
                uint16_t cs = csum(pkt, (size_t)payload);
                pkt[2] = (uint8_t)(cs >> 8);
                pkt[3] = (uint8_t)cs;
            }
            sent_at[i] = now;
            if (sendto(s, pkt, (size_t)payload, 0, (struct sockaddr *)&to, sizeof(to)) < 0) {
                if (errno == EMSGSIZE) {
                    r->status = ST_MSGSIZE;
                    ls_fmt(r->msg, sizeof(r->msg), "local mtu below %d", size);
                    close(s);
                    return;
                }
            } else {
                r->sent++;
            }
            i++;
            next = now + (uint64_t)interval * 1000u;
            if (i == count)
                end = now + 1000000u;
            continue;
        }
        if (i == count && (now >= end || r->recv == r->sent))
            break;
        wait = (int)(((i < count ? next : end) - now) / 1000) + 1;
        pfd.fd = s;
        pfd.events = POLLIN;
        if (poll(&pfd, 1, wait) <= 0)
            continue;
        {
            struct sockaddr_in from;
            socklen_t fl = sizeof(from);
            ssize_t n = recvfrom(s, in, sizeof(in), 0, (struct sockaddr *)&from, &fl);
            const uint8_t *ic = in;
            int seq;
            uint64_t rtt;
            if (n <= 0)
                continue;
            if (raw) {
                int ihl = (in[0] & 0x0f) * 4;
                if (n < ihl + 8)
                    continue;
                ic = in + ihl;
                n -= ihl;
                if (ic[4] != (uint8_t)(id >> 8) || ic[5] != (uint8_t)id)
                    continue;
            } else if (n < 8) {
                continue;
            }
            if (ic[0] != 0 || from.sin_addr.s_addr != c->dst)
                continue;
            seq = ic[6] << 8 | ic[7];
            if (seq >= i || got[seq])
                continue;
            got[seq] = 1;
            rtt = ls_now_us() - sent_at[seq];
            r->recv++;
            sum += rtt;
            if (rtt < r->rtt_min)
                r->rtt_min = (uint32_t)rtt;
            if (rtt > r->rtt_max)
                r->rtt_max = (uint32_t)rtt;
        }
    }
    close(s);
    if (r->recv) {
        r->rtt_avg = (uint32_t)(sum / r->recv);
    } else {
        r->rtt_min = 0;
        r->status = ST_UNREACHABLE;
    }
}
