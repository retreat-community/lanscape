#ifndef LSM_SERVER_H
#define LSM_SERVER_H

#include <poll.h>
#include <stddef.h>
#include <stdint.h>

#include "../common/jw.h"
#include "../common/lstp.h"

#define MAX_AGENTS 64
#define MAX_AG_IFS 32
#define MAX_SEGS 64
#define MAX_EXPECT 32

typedef struct {
    char name[16];
    uint8_t mac[6];
    uint32_t addr;
    uint8_t prefix;
    uint16_t vlan;
    uint32_t speed;
    uint32_t mtu;
    uint32_t flags;
    char kind[16];
    char parent[16];
} s_if;

typedef struct {
    char id[64];
    char version[32];
    char hostname[64];
    char host_id[64];
    char arch[16];
    uint32_t caps;
    uint16_t data_port;
    s_if ifs[MAX_AG_IFS];
    int nifs;
} agent_info;

typedef struct {
    int used;
    int fd; /* -1 when offline */
    int authed;
    uint8_t n1[LSTP_NONCE_LEN];
    uint8_t n2[LSTP_NONCE_LEN];
    agent_info info;
    uint8_t *rbuf;
    size_t rlen;
    uint32_t peer;
    uint64_t last_rx;
    uint64_t last_tx;
    uint64_t seen_ms;
} agent_t;

typedef struct {
    uint32_t net;
    int prefix;
    uint32_t mbps;
} expect_t;

typedef struct {
    char listen[64];
    char ctl_listen[64];
    char token[128];
    char data_file[256];
    char webhook[256];
    char password[128];
    uint32_t keep_runs;
    uint32_t duration_ms;
    uint32_t streams;
    uint32_t ping_count;
    uint32_t rtt_warn_ms;
    expect_t expect[MAX_EXPECT];
    int nexpect;
} server_conf;

extern server_conf g_conf;
extern agent_t g_agents[MAX_AGENTS];

/* segments */
typedef struct {
    int agent;
    int ifi;
} member_t;

typedef struct {
    uint32_t net;
    int prefix;
    uint16_t vlan;
    uint32_t manual_mbps;
    int nmem;
    member_t mem[MAX_AGENTS];
} segment_t;

int build_segments(const agent_info *const *agents, int nagents, segment_t *segs, int max);
void seg_name(const segment_t *s, char *out, size_t cap);
uint32_t expect_for(uint32_t net, int prefix);

/* agents (agents.c) */
void agents_accept(int lfd);
int agents_pollfds(struct pollfd *p, int max);
void agents_handle(int fd, short revents);
void agents_tick(void);
agent_t *agent_by_id(const char *id);
int agent_send(agent_t *a, const uint8_t *frame, size_t len);

/* runs (run.c) */
int run_start(void);
int run_active(void);
void run_cancel(void);
void run_on_frame(agent_t *a, uint8_t type, const uint8_t *pl, size_t plen);
void run_on_disconnect(agent_t *a);
void run_tick(void);
void run_state_json(void *jw);
void w_agent(jw *w, const agent_info *a, int online, uint64_t seen);
void w_segments(jw *w, const segment_t *segs, int n, const agent_info *const *ag);

/* store (store.c) */
void store_load(void);
void store_add(char *json, size_t len);
const char *store_last(size_t *len);
const char *store_get(uint32_t id, size_t *len);
uint32_t store_next_id(void);
uint32_t store_last_id(void);
void store_list_json(void *jw);

/* http (http.c) */
void http_init(void);
void http_set_password(const char *pw);
void http_accept(int lfd);
int http_pollfds(struct pollfd *p, int max);
void http_handle(int fd, short revents);
void http_tick(void);
void sse_broadcast(const char *event, const char *data, size_t len);

/* notify */
void webhook_post(const char *json, size_t len);

#endif
