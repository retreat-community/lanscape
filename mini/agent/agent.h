#ifndef LSM_AGENT_H
#define LSM_AGENT_H

#include <stdint.h>

#include "../common/lstp.h"

typedef struct {
    char server[128];
    uint16_t server_port;
    char token[128];
    char id[64];
    char exclude[256];
    uint16_t data_port;
    uint32_t max_duration_ms;
    uint32_t max_streams;
    uint32_t inventory_s;
} agent_conf;

extern agent_conf g_conf;

/* Command parameters received from the server (LC_CMD). */
typedef struct {
    uint32_t cmd_id;
    uint8_t kind;
    char dev[16];
    uint32_t src;
    uint32_t dst;
    uint16_t count;
    uint16_t interval_ms;
    uint16_t size;
    uint16_t port;
    uint32_t run_id;
    uint8_t token[LSTP_TOKEN_LEN];
    uint8_t test_kind;
    uint8_t direction;
    uint8_t streams;
    uint32_t duration_ms;
} cmd_t;

typedef struct {
    uint8_t status;
    char route_dev[16];
    uint16_t sent;
    uint16_t recv;
    uint32_t rtt_min;
    uint32_t rtt_avg;
    uint32_t rtt_max;
    uint64_t bytes;
    uint64_t window_us;
    uint64_t bps;
    uint16_t local_cpu;
    uint16_t peer_cpu;
    uint8_t path_ok;
    uint64_t if_delta;
    char msg[96];
} result_t;

/* data plane */
void dp_grant(uint32_t run_id, const uint8_t *token, uint32_t ttl_s);
void dp_respond(int cfd, int lfd);
void dp_run(const cmd_t *c, result_t *r);

/* ICMP */
void icmp_run(const cmd_t *c, result_t *r, int df);

#endif
