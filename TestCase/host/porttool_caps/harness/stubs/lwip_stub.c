/*
 * Fake TCP stack.
 *
 * The single session slot is the point. The ethernet session is a server, so
 * the test needs to say "a peer connected", "a second one tried", "the peer
 * sent this back" and "nobody is there" - the states the frame has to tell
 * apart. Everything else about TCP is absent on purpose.
 *
 * *** Nothing here retransmits, windows, or fragments. The send buffer is a
 * *** fixed size that empties on every write, so source mode's pump loop runs
 * *** exactly once per tick here - enough to prove it writes and stops, not
 * *** what throughput a real link reaches. That number only exists on a board.
 */

#include "lwip_fake.h"

#include <string.h>
#include <stdio.h>

int      test_eth_lwip_inits;
int      test_eth_lwip_polls;
int      test_eth_listening;
uint16_t test_eth_bound_port;
int      test_eth_dhcp_running;
char     test_eth_sent[512];
uint32_t test_eth_sent_len;

struct tcp_pcb {
    int            used;
    int            is_listen;
    void          *arg;
    tcp_recv_fn    recv;
    tcp_accept_fn  accept;
    tcp_err_fn     err;
    uint16_t       port;
};

/* One listener and one client is all the session can ever hold. */
static struct tcp_pcb pcbs[3];

static struct netif the_netif;
struct netif *netif_default;

static struct tcp_pcb *listen_pcb;
static struct tcp_pcb *client_pcb;

/* How much source mode may hand over per write. Small enough that the pump
 * loop's "keep going until the window is full" path is actually exercised. */
#define FAKE_SNDBUF 256

static struct tcp_pcb *alloc_pcb(void)
{
    for (size_t i = 0; i < sizeof(pcbs) / sizeof(pcbs[0]); i++) {
        if (!pcbs[i].used) {
            memset(&pcbs[i], 0, sizeof(pcbs[i]));
            pcbs[i].used = 1;
            return &pcbs[i];
        }
    }
    return NULL;
}

/* ---- what the firmware calls ------------------------------------------- */

struct tcp_pcb *tcp_new(void) { return alloc_pcb(); }

err_t tcp_bind(struct tcp_pcb *pcb, const ip4_addr_t *ipaddr, u16_t port)
{
    (void)ipaddr;
    if (pcb == NULL) { return ERR_VAL; }
    pcb->port = port;
    return ERR_OK;
}

struct tcp_pcb *tcp_listen(struct tcp_pcb *pcb)
{
    if (pcb == NULL) { return NULL; }
    pcb->is_listen = 1;
    listen_pcb = pcb;
    test_eth_listening = 1;
    test_eth_bound_port = pcb->port;
    return pcb;
}

void tcp_accept(struct tcp_pcb *pcb, tcp_accept_fn accept)
{
    if (pcb != NULL) { pcb->accept = accept; }
}

void tcp_arg(struct tcp_pcb *pcb, void *arg)        { if (pcb) pcb->arg  = arg;  }
void tcp_recv(struct tcp_pcb *pcb, tcp_recv_fn r)   { if (pcb) pcb->recv = r;    }
void tcp_sent(struct tcp_pcb *pcb, tcp_sent_fn s)   { (void)pcb; (void)s;        }
void tcp_err(struct tcp_pcb *pcb, tcp_err_fn e)     { if (pcb) pcb->err  = e;    }

err_t tcp_close(struct tcp_pcb *pcb)
{
    if (pcb == NULL) { return ERR_VAL; }
    if (pcb == listen_pcb) { listen_pcb = NULL; test_eth_listening = 0; }
    if (pcb == client_pcb) { client_pcb = NULL; }
    pcb->used = 0;
    return ERR_OK;
}

void tcp_abort(struct tcp_pcb *pcb) { (void)tcp_close(pcb); }

err_t tcp_write(struct tcp_pcb *pcb, const void *data, u16_t len, uint8_t flags)
{
    (void)flags;
    if ((pcb == NULL) || !pcb->used) { return ERR_VAL; }

    /* Kept so a test can read back what the board put on the wire. The tail is
     * dropped rather than wrapped: source mode writes far more than any
     * assertion needs, and a wrapped buffer would make the echo lines it
     * really does check unfindable. */
    if (test_eth_sent_len < sizeof(test_eth_sent) - 1U) {
        uint32_t room = (uint32_t)(sizeof(test_eth_sent) - 1U - test_eth_sent_len);
        uint32_t n = (len < room) ? len : room;
        memcpy(test_eth_sent + test_eth_sent_len, data, n);
        test_eth_sent_len += n;
        test_eth_sent[test_eth_sent_len] = '\0';
    }
    return ERR_OK;
}

err_t tcp_output(struct tcp_pcb *pcb) { (void)pcb; return ERR_OK; }
void  tcp_recved(struct tcp_pcb *pcb, u16_t len) { (void)pcb; (void)len; }

u16_t tcp_sndbuf(struct tcp_pcb *pcb)
{
    /* One window's worth, then nothing: the pump has to stop on its own. */
    static int handed_out;

    if (pcb == NULL) { return 0; }
    if (handed_out) { handed_out = 0; return 0; }
    handed_out = 1;
    return FAKE_SNDBUF;
}

void pbuf_free(struct pbuf *p) { (void)p; }

char *ip4addr_ntoa(const ip4_addr_t *addr)
{
    static char buf[16];
    uint32_t a;

    if (addr == NULL) { return "0.0.0.0"; }
    a = PP_HTONL(addr->addr);
    snprintf(buf, sizeof(buf), "%u.%u.%u.%u",
             (unsigned)((a >> 24) & 0xFFu), (unsigned)((a >> 16) & 0xFFu),
             (unsigned)((a >> 8) & 0xFFu),  (unsigned)(a & 0xFFu));
    return buf;
}

int ip4addr_aton(const char *cp, ip4_addr_t *addr)
{
    unsigned a, b, c, d;
    char tail;

    if ((cp == NULL) || (addr == NULL)) { return 0; }
    if (sscanf(cp, "%u.%u.%u.%u%c", &a, &b, &c, &d, &tail) != 4) { return 0; }
    if ((a > 255u) || (b > 255u) || (c > 255u) || (d > 255u)) { return 0; }

    IP4_ADDR(addr, a, b, c, d);
    return 1;
}

void netif_set_addr(struct netif *netif, const ip4_addr_t *ip,
                    const ip4_addr_t *netmask, const ip4_addr_t *gw)
{
    if (netif == NULL) { return; }
    if (ip)      { netif->ip_addr = *ip; }
    if (netmask) { netif->netmask = *netmask; }
    if (gw)      { netif->gw      = *gw; }
}

err_t dhcp_start(struct netif *netif) { (void)netif; test_eth_dhcp_running = 1; return ERR_OK; }
void  dhcp_stop(struct netif *netif)  { (void)netif; test_eth_dhcp_running = 0; }

int dhcp_supplied_address(const struct netif *netif)
{
    return test_eth_dhcp_running && (netif != NULL) && (netif->ip_addr.addr != 0u);
}

void MX_LWIP_Init(void)
{
    test_eth_lwip_inits++;
    netif_default = &the_netif;
    test_eth_dhcp_running = 1;
}

void MX_LWIP_Process(void) { test_eth_lwip_polls++; }

/* ---- what the test drives ---------------------------------------------- */

int test_eth_connect(void)
{
    struct tcp_pcb *pcb;

    if ((listen_pcb == NULL) || (listen_pcb->accept == NULL)) { return 0; }

    pcb = alloc_pcb();
    if (pcb == NULL) { return 0; }

    if (listen_pcb->accept(NULL, pcb, ERR_OK) != ERR_OK) {
        pcb->used = 0;          /* refused - lwIP would abort it */
        return 0;
    }
    client_pcb = pcb;
    return 1;
}

void test_eth_feed(const char *s)
{
    struct pbuf p;

    if ((client_pcb == NULL) || (client_pcb->recv == NULL) || (s == NULL)) {
        return;
    }
    p.next    = NULL;
    p.payload = (void *)(size_t)s;
    p.len     = (uint16_t)strlen(s);
    p.tot_len = p.len;
    (void)client_pcb->recv(client_pcb->arg, client_pcb, &p, ERR_OK);
}

void test_eth_disconnect(void)
{
    struct tcp_pcb *pcb = client_pcb;

    if ((pcb == NULL) || (pcb->recv == NULL)) { return; }
    /* lwIP signals a closed peer with a NULL pbuf. */
    (void)pcb->recv(pcb->arg, pcb, NULL, ERR_OK);
    client_pcb = NULL;
}

void test_eth_set_link(int up) { the_netif.link_up = up ? 1 : 0; }

void test_eth_set_dhcp_address(const char *dotted)
{
    (void)ip4addr_aton(dotted, &the_netif.ip_addr);
}

void test_eth_reset(void)
{
    memset(pcbs, 0, sizeof(pcbs));
    memset(&the_netif, 0, sizeof(the_netif));
    listen_pcb = NULL;
    client_pcb = NULL;
    netif_default = NULL;
    test_eth_lwip_inits = 0;
    test_eth_lwip_polls = 0;
    test_eth_listening = 0;
    test_eth_bound_port = 0;
    test_eth_dhcp_running = 0;
    test_eth_sent[0] = '\0';
    test_eth_sent_len = 0;
}
