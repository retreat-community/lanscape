/* LSTP/1 constants shared by lsm-agent, lsm-server and the Go implementation
 * (internal/proto). See docs/PROTOCOL.md. */
#ifndef LS_LSTP_H
#define LS_LSTP_H

#define LSTP_VERSION 1
#define LSTP_HDR 6
#define LSTP_MAX_PAYLOAD 65535
#define LSTP_NONCE_LEN 16
#define LSTP_TOKEN_LEN 32
#define LSTP_DATA_PORT 47700
#define LSTP_CTL_PORT 47701
#define LSTP_TOKEN_TTL_S 600
#define LSTP_WARMUP_MS 1000

/* data-plane frame types */
enum {
    LS_HELLO = 0x01,
    LS_CHALLENGE = 0x02,
    LS_AUTH = 0x03,
    LS_READY = 0x04,
    LS_START = 0x05,
    LS_RESULT = 0x06,
    LS_SENDER_STAT = 0x07,
    LS_ECHO_REQ = 0x08,
    LS_ECHO_REP = 0x09,
    LS_ERROR = 0x0a,
    LS_BYE = 0x0b,
};

/* control-plane frame types (Mini agent <-> server, port 47701) */
enum {
    LC_HELLO = 0x20,
    LC_CHALLENGE = 0x21,
    LC_AUTH = 0x22,
    LC_WELCOME = 0x23,
    LC_INVENTORY = 0x24,
    LC_PING = 0x25,
    LC_PONG = 0x26,
    LC_GRANT = 0x27,
    LC_GRANT_ACK = 0x28,
    LC_CMD = 0x29,
    LC_CMD_RESULT = 0x2a,
    LC_CANCEL = 0x2b,
};

/* tags */
enum {
    T_AGENT_ID = 0x01,
    T_RUN_ID = 0x02,
    T_TEST_KIND = 0x03,
    T_DIRECTION = 0x04,
    T_STREAMS = 0x05,
    T_DURATION_MS = 0x06,
    T_NONCE = 0x07,
    T_MAC = 0x08,
    T_ROLE = 0x09,
    T_SESSION = 0x0a,
    T_STREAM_IDX = 0x0b,
    T_BYTES = 0x0c,
    T_WINDOW_US = 0x0d,
    T_CPU = 0x0e,
    T_SEQ = 0x0f,
    T_TS_US = 0x10,
    T_ERR_CODE = 0x11,
    T_ERR_MSG = 0x12,
    T_UDP_RATE_KBPS = 0x13,
    T_UDP_PORT = 0x14,
    T_PKT_SIZE = 0x15,
    T_PKTS_SENT = 0x16,
    T_PKTS_RECV = 0x17,
    T_JITTER_US = 0x18,
    T_LOST = 0x19,
    T_IF_TX = 0x1a,
    T_IF_RX = 0x1b,
    T_PATH_OK = 0x1c,
    T_VERSION = 0x1d,
    T_WARMUP_MS = 0x1e,

    T_IFACE = 0x40,
    T_IF_NAME = 0x41,
    T_IF_MAC = 0x42,
    T_IF_ADDR4 = 0x43,
    T_IF_PREFIX = 0x44,
    T_IF_VLAN = 0x45,
    T_IF_SPEED = 0x46,
    T_IF_MTU = 0x47,
    T_IF_FLAGS = 0x48,
    T_IF_KIND = 0x49,
    T_IF_PARENT = 0x4a,
    T_HOSTNAME = 0x4b,
    T_HOST_ID = 0x4c,
    T_ARCH = 0x4d,
    T_CAPS = 0x4e,

    T_CMD_ID = 0x50,
    T_CMD_KIND = 0x51,
    T_SRC_DEV = 0x52,
    T_SRC_ADDR = 0x53,
    T_DST_ADDR = 0x54,
    T_COUNT = 0x55,
    T_INTERVAL_MS = 0x56,
    T_SIZE = 0x57,
    T_RUN_TOKEN = 0x58,
    T_TTL_S = 0x59,
    T_STATUS = 0x5a,
    T_RTT_MIN_US = 0x5b,
    T_RTT_AVG_US = 0x5c,
    T_RTT_MAX_US = 0x5d,
    T_SENT = 0x5e,
    T_RECV = 0x5f,
    T_PEER_CPU = 0x60,
    T_BPS = 0x61,
    T_ROUTE_DEV = 0x62,
    T_RTT_P95_US = 0x63,
    T_SERVER_MAC = 0x65,
    T_LOCAL_CPU = 0x66,
    T_DST_PORT = 0x67,
};

/* test kinds */
enum { TK_TCP_THROUGHPUT = 1, TK_UDP_THROUGHPUT = 2, TK_ECHO = 3 };
/* directions */
enum { DIR_FORWARD = 0, DIR_REVERSE = 1, DIR_BIDIR = 2 };
/* roles */
enum { ROLE_CTRL = 0, ROLE_DATA = 1 };
/* control command kinds */
enum { CK_PING = 1, CK_PMTU = 2, CK_LSTP = 3 };
/* capability bits */
enum { CAP_LITE = 1, CAP_ICMP = 2, CAP_TCP = 4, CAP_UDP = 8 };
/* interface flags */
enum { IFF_LS_UP = 1, IFF_LS_CARRIER = 2 };

/* command/test status */
enum {
    ST_OK = 0,
    ST_ROUTE_MISMATCH = 1,
    ST_UNREACHABLE = 2,
    ST_REFUSED = 3,
    ST_PROTOCOL = 4,
    ST_AUTH_FAIL = 5,
    ST_TIMEOUT = 6,
    ST_COUNTER_MISMATCH = 7,
    ST_NO_DEVICE = 8,
    ST_UNSUPPORTED = 9,
    ST_INTERNAL = 10,
    ST_BUSY = 11,
    ST_MSGSIZE = 12,
    ST_LIMIT = 13,
};

/* data-plane error codes */
enum { E_AUTH = 1, E_UNKNOWN_RUN = 2, E_UNSUPPORTED = 3, E_LIMIT = 4, E_BUSY = 5, E_PROTO = 6 };

#endif
