/*
 * A simulated board: the real port tool firmware, on a PC, talking over a pipe.
 *
 * The point is that everything above the peripheral registers is the firmware's
 * own source. The command parser, pt.caps, every session's start/set/stop, the
 * echo counter, the frame format and the version string all come from
 * every TestCase/porttool source compiled unchanged. Only the stubs underneath are fake.
 * So a panel that works against this is working against the real protocol, and
 * a protocol change breaks this the same day it is made - which a hand-written
 * fake board in Go could never promise.
 *
 * *** This is for wiring up the PC side without hardware. It is NOT evidence
 * *** about a board. Every reading below is invented: the stubs model an
 * *** ideally behaved unit with every peer cable plugged in. Nothing here can
 * *** fail the way real hardware fails, so a plan that passes against this has
 * *** only been shown to be well-formed.
 *
 * Differences from PortTool_Run() that matter:
 *
 *   - the command channel is stdin/stdout rather than UART4 on interrupt, so
 *     the ring buffer and rx_errors path are not exercised here
 *   - the clock is the wall clock rather than the HAL tick
 *   - stimulate() plays the peers: it answers the loop=link ports the way the
 *     RS485 adapter, the CAN adapter and a TCP peer would
 *
 * Build: python build.py --sim   (same sources and stubs as the T4-01 harness)
 */

#include "porttool.c"

#include "relay.h"
#include "port_dout.h"
#include "port_adc.h"
#include "lwip_fake.h"
#include "usb_device.h"
#include "usbd_def.h"

#include <pthread.h>
#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <time.h>

#ifdef _WIN32
#include <windows.h>
#include <io.h>
#include <fcntl.h>
#else
#include <unistd.h>
#endif

extern uint8_t  test_din_bits;
extern uint32_t test_vdda_mv;
extern uint32_t test_ain_raw[PORT_AIN_COUNT];
extern uint32_t test_ain_mv[PORT_AIN_COUNT];
extern uint32_t test_temp_mv[PORT_TEMP_COUNT];
extern uint32_t test_uid_words[3];

extern uint32_t test_dout_duty[PORT_DOUT_COUNT];

extern int      test_eth_link;      /* the PHY's own link bit, read over MDIO */

extern char     test_rs485_sent[256];
extern uint32_t test_rs485_sent_len;
extern void     test_rs485_reply(const char *line);

extern void     test_set_tick(uint32_t ms);

/* ---- the command channel ------------------------------------------------
 *
 * A reader thread rather than a non-blocking read: stdin here is a pipe on one
 * platform and a console on another, and the portable way to poll those is
 * different for each. A thread is the same code everywhere, and the queue
 * between it and the loop is the only shared state.
 */

#define QDEPTH 32

static pthread_mutex_t q_lock = PTHREAD_MUTEX_INITIALIZER;
static char            q[QDEPTH][PORTTOOL_LINE_MAX];
static int             q_head, q_tail;
static volatile int    input_closed;

static void *reader(void *arg)
{
    char line[PORTTOOL_LINE_MAX * 2];

    (void)arg;
    while (fgets(line, sizeof(line), stdin) != NULL) {
        size_t n = strlen(line);
        while (n > 0 && (line[n - 1] == '\n' || line[n - 1] == '\r')) {
            line[--n] = '\0';
        }
        if (n == 0) {
            continue;
        }
        /* Anything past the board's line limit is dropped, not wrapped into a
         * second command - the same thing the real receive path does. */
        if (n >= PORTTOOL_LINE_MAX) {
            n = PORTTOOL_LINE_MAX - 1;
        }

        pthread_mutex_lock(&q_lock);
        int next = (q_head + 1) % QDEPTH;
        if (next != q_tail) {
            memcpy(q[q_head], line, n);
            q[q_head][n] = '\0';
            q_head = next;
        }
        /* A full queue drops the newest, matching the board: its ring counts a
         * drop rather than overwriting a command already half-read. */
        pthread_mutex_unlock(&q_lock);
    }
    input_closed = 1;
    return NULL;
}

static int next_command(char *out, size_t out_len)
{
    int got = 0;

    pthread_mutex_lock(&q_lock);
    if (q_tail != q_head) {
        snprintf(out, out_len, "%s", q[q_tail]);
        q_tail = (q_tail + 1) % QDEPTH;
        got = 1;
    }
    pthread_mutex_unlock(&q_lock);
    return got;
}

/* ---- the clock ---------------------------------------------------------- */

static uint64_t wall_ms(void)
{
#ifdef _WIN32
    return (uint64_t)GetTickCount64();
#else
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000u + (uint64_t)(ts.tv_nsec / 1000000);
#endif
}

static void nap_ms(unsigned ms)
{
#ifdef _WIN32
    Sleep(ms);
#else
    struct timespec ts = { ms / 1000u, (long)(ms % 1000u) * 1000000L };
    nanosleep(&ts, NULL);
#endif
}

/* ---- the peers ----------------------------------------------------------
 *
 * Every loop=link port is answered here the way its real peer would: the
 * number the board sent, sent straight back, unchanged (DECISIONS.md 18).
 * Without this the panel would show miss climbing on every link port, which is
 * what an unwired bench looks like - true of a bench, wrong for a fake board
 * whose whole purpose is to stand in for a fully wired one.
 */

/* Reads the last complete decimal number out of a buffer the board wrote. */
static int last_number(const char *buf, uint32_t len, uint32_t *out)
{
    int have = 0;
    uint32_t acc = 0;
    int in_num = 0;

    for (uint32_t i = 0; i < len; i++) {
        char c = buf[i];
        if (c >= '0' && c <= '9') {
            acc = acc * 10u + (uint32_t)(c - '0');
            in_num = 1;
        } else if (in_num) {
            *out = acc; have = 1; acc = 0; in_num = 0;
        }
    }
    if (in_num) { *out = acc; have = 1; }
    return have;
}

static void peer_rs485(void)
{
    char line[32];
    uint32_t v;

    if (test_rs485_sent_len == 0u) {
        return;
    }
    if (last_number(test_rs485_sent, test_rs485_sent_len, &v)) {
        snprintf(line, sizeof(line), "%lu", (unsigned long)v);
        test_rs485_reply(line);
    }
    test_rs485_sent_len = 0;
    test_rs485_sent[0] = '\0';
}

static void peer_eth(void)
{
    char line[32];
    uint32_t v;

    /* Connect once the server is listening, the way a peer with the address
     * would. Nothing reconnects it: a session restart rebuilds the listener,
     * and test_eth_connect is a no-op while one is already attached. */
    if (test_eth_listening) {
        /* A cable and a lease. Both have to be set after MX_LWIP_Init, because
         * that is what creates the interface these describe - and without them
         * the frame reports ip=0.0.0.0 link=0, which on a bench means an
         * unplugged cable rather than a working one. */
        test_eth_set_link(1);
        test_eth_set_dhcp_address("192.168.1.50");
        (void)test_eth_connect();
    }
    if (test_eth_sent_len == 0u) {
        return;
    }
    if (last_number(test_eth_sent, test_eth_sent_len, &v)) {
        snprintf(line, sizeof(line), "%lu\n", (unsigned long)v);
        test_eth_feed(line);
    }
    test_eth_sent_len = 0;
    test_eth_sent[0] = '\0';
}

/* The PC at the other end of the CDC pipe, doing what the panel's link
 * responder does: sending back the number the board just sent. */
static void peer_usb(void)
{
    char line[32];
    uint32_t v;

    if (test_usb_sent_len == 0u) {
        return;
    }
    if (last_number(test_usb_sent, test_usb_sent_len, &v)) {
        snprintf(line, sizeof(line), "%lu\n", (unsigned long)v);
        test_usb_feed(line);
    }
    test_usb_sent_len = 0;
    test_usb_sent[0] = '\0';
}

/* ---- what the board is looking at --------------------------------------- */

/* Off by default: a fixture holds every digital input high, and that is the
 * state a production plan is written against. Turned on by hand when somebody
 * wants to watch the panel react to something changing. */
static int sim_walk;

static void stimulate(uint32_t now_ms)
{
    if (sim_walk) {
        static const uint8_t walk[4] = { 0x16u, 0xA5u, 0x3Cu, 0xFFu };
        test_din_bits = walk[(now_ms / 3000u) % 4u];
    }

    peer_rs485();
    peer_eth();
    peer_usb();
}

/* ---- the sim.* commands -------------------------------------------------
 *
 * These never reach dispatch(). They are how a person makes this board behave
 * like a broken one, which is the only way to see what the panel does with a
 * failure without breaking real hardware.
 *
 * *** The prefix is the promise: nothing starting with "sim." exists on a
 * *** board, and the firmware's command table is not touched to support any of
 * *** it. A panel that came to depend on one of these would be depending on
 * *** something no board can do.
 */
static int sim_command(const char *line)
{
    unsigned a = 0, b = 0;

    if (strncmp(line, "sim.", 4) != 0) {
        return 0;
    }

    if (strcmp(line, "sim.help") == 0) {
        printf("SIM  sim.din <hex>      what the digital inputs read, e.g. sim.din 0xFF\r\n"
               "SIM  sim.walk <0|1>     cycle the inputs through four patterns\r\n"
               "SIM  sim.vdda <mv>      the ADC reference, e.g. sim.vdda 1800 to fail it\r\n"
               "SIM  sim.ain <ch> <mv>  one analog input's reading\r\n"
               "SIM  sim.temp <ch> <mv> one temperature sensor's reading\r\n"
               "SIM  sim.link <0|1>     whether the ethernet PHY sees a cable\r\n"
               "SIM  none of these exist on a board\r\n");
        return 1;
    }
    if (sscanf(line, "sim.din %i", (int *)&a) == 1) {
        test_din_bits = (uint8_t)a;
        printf("SIM  din=0x%02X\r\n", (unsigned)test_din_bits);
        return 1;
    }
    if (sscanf(line, "sim.walk %u", &a) == 1) {
        sim_walk = (a != 0u);
        printf("SIM  walk=%d\r\n", sim_walk);
        return 1;
    }
    if (sscanf(line, "sim.vdda %u", &a) == 1) {
        test_vdda_mv = a;
        printf("SIM  vdda=%lu\r\n", (unsigned long)test_vdda_mv);
        return 1;
    }
    if (sscanf(line, "sim.ain %u %u", &a, &b) == 2 && a >= 1u && a <= PORT_AIN_COUNT) {
        test_ain_mv[a - 1] = b;
        printf("SIM  ain%u=%u mV\r\n", a, b);
        return 1;
    }
    if (sscanf(line, "sim.temp %u %u", &a, &b) == 2 && a >= 1u && a <= PORT_TEMP_COUNT) {
        test_temp_mv[a - 1] = b;
        printf("SIM  temp%u=%u mV\r\n", a, b);
        return 1;
    }
    if (sscanf(line, "sim.link %u", &a) == 1) {
        /* Both of them: the PHY's own BSR, which eth.link reads over MDIO, and
         * the netif the session reports. On a board those are two views of one
         * cable, so a backdoor that moved only one would show a state no board
         * can be in. */
        test_eth_link = (a != 0u);
        test_eth_set_link(a != 0u);
        printf("SIM  link=%u\r\n", a != 0u);
        return 1;
    }

    printf("SIM  no such sim command - try sim.help\r\n");
    return 1;
}

int main(int argc, char **argv)
{
    pthread_t tid;
    uint64_t  started;
    char      cmd[PORTTOOL_LINE_MAX];

    (void)argc; (void)argv;

#ifdef _WIN32
    /* The firmware writes "\r\n" itself; text mode would make it "\r\r\n". */
    _setmode(_fileno(stdout), _O_BINARY);
#endif
    /* Line buffered, so the panel sees a frame the moment it is written rather
     * than when a 4 KB buffer happens to fill. */
    setvbuf(stdout, NULL, _IOLBF, 4096);

    /* A good unit, fully wired.
     *
     * These are the values a healthy board gives, not the stub defaults the
     * transcript test needs: vdda is the 2.5 V VREFBUF reference a real board
     * reads 2501 mV on, and the analog inputs are placed inside their bands
     * rather than in the floating window. Somebody comparing a simulated run
     * against a bench run has to be able to see the same verdicts. */
    test_vdda_mv   = 2501u;
    /* A fixture holding every digital input high, which is what a production
     * plan is written against. sim.din changes it. */
    test_din_bits  = 0xFFu;
    test_ain_mv[0] = 2071u;  test_ain_raw[0] = 21850u;
    test_ain_mv[1] = 1240u;  test_ain_raw[1] = 13100u;
    test_temp_mv[0] = 738u;  test_temp_mv[1] = 751u;

    /* An obviously invented UID. Nobody should be able to mistake a simulated
     * run's report for a board's, and the serial number is what a report is
     * filed under. */
    test_uid_words[0] = 0x00FACADEu;
    test_uid_words[1] = 0x00FACADEu;
    test_uid_words[2] = 0x00FACADEu;

    started = wall_ms();
    test_set_tick(1000u);

    if (pthread_create(&tid, NULL, reader, NULL) != 0) {
        fprintf(stderr, "sim board: could not start the reader thread\n");
        return 1;
    }

    /* Printed as a bare log line, which the panel shows in its log pane. Said
     * as loudly as a line of text can: the readings below are invented. */
    printf("\r\n=== SIMULATED port tool %s - NOT A BOARD ===\r\n"
           "Every reading is invented by a stub. Use this to wire up the PC\r\n"
           "side; it is not evidence about hardware.\r\n\r\n",
           PORTTOOL_VERSION);
    fflush(stdout);

    for (;;) {
        uint32_t now = (uint32_t)(wall_ms() - started) + 1000u;

        test_set_tick(now);

        /* Every queued command, so a burst is not spread over several ticks. */
        while (next_command(cmd, sizeof(cmd))) {
            /* poll_command() counts a line here on the board, and the rs232
             * session reports that count as rxlines - its evidence that the
             * receive direction is carrying traffic. The command channel is a
             * pipe in this build, so the count has to be kept by hand or that
             * session reports a dead port on a board that is answering. */
            lines_in++;
            if (!sim_command(cmd)) {
                dispatch(cmd);
            }
        }

        tick_sessions(now);
        stimulate(now);
        PortEth_Poll();

        fflush(stdout);

        if (input_closed) {
            /* The panel closed the pipe. Nothing else can arrive, so stop
             * rather than spin printing frames into a closed handle. */
            break;
        }
        nap_ms(5);
    }

    return 0;
}
