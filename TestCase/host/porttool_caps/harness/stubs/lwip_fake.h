/*
 * Stand-in for the parts of lwIP that porttool_eth.c touches.
 *
 * Only the ethernet session needs a network stack, and building the real one
 * for a host test would drag in the ETH DMA and a netif implementation to
 * prove nothing about the protocol. What the harness has to be able to say is
 * "a peer connected", "it sent this back", "nobody connected" - which is a
 * queue and a flag, exactly like the RS485 stub.
 *
 * Only the surface the session uses is declared. A future porttool_eth.c that
 * reaches for more lwIP fails loudly here instead of quietly not being covered.
 *
 * The four real headers the session includes - lwip.h, lwip/tcp.h,
 * lwip/netif.h, lwip/dhcp.h - are thin files next to this one that include it,
 * so the firmware's own #include lines compile unchanged.
 */

#ifndef PORTTOOL_HOSTTEST_STUB_LWIP_H_
#define PORTTOOL_HOSTTEST_STUB_LWIP_H_

#include <stdint.h>
#include <stddef.h>

typedef uint16_t u16_t;
typedef int8_t   err_t;

#define ERR_OK    0
#define ERR_MEM  (-1)
#define ERR_VAL  (-6)

#define TCP_WRITE_FLAG_COPY 0x01

/* Network byte order, like the real one. */
typedef struct { uint32_t addr; } ip4_addr_t;

#define PP_HTONL(x) ((uint32_t)((((x) & 0xffUL) << 24) | \
                                (((x) & 0xff00UL) << 8) | \
                                (((x) & 0xff0000UL) >> 8) | \
                                (((x) & 0xff000000UL) >> 24)))

#define IP4_ADDR(dst, a, b, c, d) \
    ((dst)->addr = PP_HTONL(((uint32_t)(a) << 24) | ((uint32_t)(b) << 16) | \
                            ((uint32_t)(c) << 8)  |  (uint32_t)(d)))

#define ip4_addr_get_u32(a)     ((a)->addr)
#define ip4_addr_set_u32(a, v)  ((a)->addr = (v))

#define IP_ADDR_ANY ((const ip4_addr_t *)0)

char *ip4addr_ntoa(const ip4_addr_t *addr);
int   ip4addr_aton(const char *cp, ip4_addr_t *addr);

struct pbuf {
    struct pbuf *next;
    void        *payload;
    uint16_t     tot_len;
    uint16_t     len;
};

void pbuf_free(struct pbuf *p);

struct tcp_pcb;

typedef err_t (*tcp_recv_fn)(void *arg, struct tcp_pcb *pcb, struct pbuf *p, err_t err);
typedef err_t (*tcp_accept_fn)(void *arg, struct tcp_pcb *pcb, err_t err);
typedef err_t (*tcp_sent_fn)(void *arg, struct tcp_pcb *pcb, u16_t len);
typedef void  (*tcp_err_fn)(void *arg, err_t err);

struct tcp_pcb *tcp_new(void);
err_t  tcp_bind(struct tcp_pcb *pcb, const ip4_addr_t *ipaddr, u16_t port);
struct tcp_pcb *tcp_listen(struct tcp_pcb *pcb);
void   tcp_accept(struct tcp_pcb *pcb, tcp_accept_fn accept);
void   tcp_arg(struct tcp_pcb *pcb, void *arg);
void   tcp_recv(struct tcp_pcb *pcb, tcp_recv_fn recv);
void   tcp_sent(struct tcp_pcb *pcb, tcp_sent_fn sent);
void   tcp_err(struct tcp_pcb *pcb, tcp_err_fn err);
err_t  tcp_close(struct tcp_pcb *pcb);
void   tcp_abort(struct tcp_pcb *pcb);
err_t  tcp_write(struct tcp_pcb *pcb, const void *data, u16_t len, uint8_t flags);
err_t  tcp_output(struct tcp_pcb *pcb);
void   tcp_recved(struct tcp_pcb *pcb, u16_t len);
u16_t  tcp_sndbuf(struct tcp_pcb *pcb);

struct netif {
    ip4_addr_t ip_addr;
    ip4_addr_t netmask;
    ip4_addr_t gw;
    uint8_t    link_up;
};

extern struct netif *netif_default;

#define netif_ip4_addr(nif)   (&(nif)->ip_addr)
#define netif_is_link_up(nif) ((nif)->link_up)

void netif_set_addr(struct netif *netif, const ip4_addr_t *ip,
                    const ip4_addr_t *netmask, const ip4_addr_t *gw);

err_t dhcp_start(struct netif *netif);
void  dhcp_stop(struct netif *netif);
int   dhcp_supplied_address(const struct netif *netif);

void MX_LWIP_Init(void);
void MX_LWIP_Process(void);

/* ---- Test-visible state, the way rs485_stub.c does it ------------------- */

extern int      test_eth_lwip_inits;    /* MX_LWIP_Init calls - must stay 1 */
extern int      test_eth_lwip_polls;    /* MX_LWIP_Process calls */
extern int      test_eth_listening;     /* a listen pcb exists */
extern uint16_t test_eth_bound_port;    /* what it bound to */
extern int      test_eth_dhcp_running;
extern char     test_eth_sent[512];     /* everything the board wrote */
extern uint32_t test_eth_sent_len;

/* The peer connects, sends, and goes away. */
int  test_eth_connect(void);            /* 0 when the server refused */
void test_eth_feed(const char *s);
void test_eth_disconnect(void);

/* Pretend the cable is in, and give the interface an address. */
void test_eth_set_link(int up);
void test_eth_set_dhcp_address(const char *dotted);

void test_eth_reset(void);

#endif /* PORTTOOL_HOSTTEST_STUB_LWIP_H_ */
