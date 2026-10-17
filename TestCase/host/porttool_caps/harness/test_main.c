/*
 * Drives the real port tool command dispatcher on the host and prints exactly
 * what it would write to the RS232 line; firmware_test.go checks the transcript.
 *
 * porttool.c is #included so its static functions are reachable without a
 * test-only entry point; it must NOT also be handed to the compiler.
 */

#include "porttool.c"

#include "relay.h"      /* RELAY_COUNT, for the actuator check below */
#include "port_dout.h"  /* PORT_DOUT_COUNT */
#include "lwip_fake.h" /* the fake TCP stack the eth session runs on */
#include "usb_device.h" /* the fake CDC device the usb session runs on */
#include "usbd_def.h"

#include <stdio.h>
#include <string.h>

#ifdef _WIN32
#include <io.h>
#include <fcntl.h>
#endif

extern int     test_relay_energised[RELAY_COUNT];
extern int     test_relay_init_count;
extern int     test_din_init_count;
extern uint8_t test_din_bits;

extern void     test_rs485_reply(const char *line);
extern void     test_rs485_reset(void);
extern char     test_rs485_sent[256];
extern int      test_rs485_driving;
extern uint32_t test_rs485_baud;

extern int      test_sdram_ready;
extern int      test_sdram_probe_count;

extern int      test_sd_detected;
extern int      test_sd_identical;

extern int      test_can_open_count;
extern int      test_can_alive;
extern uint8_t  test_can_rate;
extern uint32_t test_can_mode;
extern uint32_t test_can_sent;
extern int      test_can_loopback;
extern uint32_t test_can_last_id;
extern uint8_t  test_can_last_data[8];
extern uint8_t  test_can_last_len;
void test_can_inject(uint32_t id, const uint8_t *data, uint8_t len);

extern uint32_t test_reset_rsr;
extern const char *test_reset_cause;
extern int      test_rtc_init_count;
extern int      test_rtc_calendar_initialised;
extern int      test_led_configured;
extern int      test_led_writes;
extern int      test_led_high;

extern int      test_vref_fails;
extern int      test_vref_enable_count;
extern uint32_t test_dac_mv[2];
extern int      test_aout_ef[2];

extern uint32_t test_dout_duty[PORT_DOUT_COUNT];
extern int      test_dout_stop_count;
extern int      test_dout_running;

extern void test_set_tick(uint32_t ms);

extern void     test_uart_raise(uint32_t isr_bits);
extern uint32_t test_uart_isr(void);
extern void     test_uart_feed(uint8_t b);
void            UART4_IRQHandler(void);
extern uint32_t test_uart4_baud;

/* One received line, then what the superloop does after poll_command(). */
static void run(const char *line)
{
    printf(">>> %s\n", line);
    line_heard(HAL_GetTick());
    dispatch(line);
    line_apply(HAL_GetTick());
}

/* How much of the board is still driven. */
static int relays_energised(void)
{
    int n = 0;
    for (int i = 0; i < RELAY_COUNT; i++) {
        n += test_relay_energised[i] ? 1 : 0;
    }
    return n;
}

static int dout_duty_any(void)
{
    for (int i = 0; i < PORT_DOUT_COUNT; i++) {
        if (test_dout_duty[i] != 0u) {
            return 1;
        }
    }
    return 0;
}

/* Moves the fake clock forward and runs one superloop pass. */
static uint32_t fake_now = 1000u;

static void advance(uint32_t ms)
{
    fake_now += ms;
    test_set_tick(fake_now);
    printf(">>> (host) %lu ms later\n", (unsigned long)ms);
    hold_check(fake_now);
    line_check(fake_now);
    tick_sessions(fake_now);
}

int main(void)
{
#ifdef _WIN32
    /* The firmware writes "\r\n" itself; text mode would make it "\r\r\n". */
    _setmode(_fileno(stdout), _O_BINARY);
#endif

    /* The panel is built from this, so it is the first thing to lock down. */
    run("pt.caps");
    run("pt.id");

    /* pt.cal, written then erased. The coefficients are exact in float, so the
     * Go side compares them for equality. */
    {
        static const float coef[8] = {
            1.0625f, -12.5f,    /* AI1 */
            0.96875f, 0.125f,   /* AI2 */
            1.03125f, -0.25f,   /* AO1 */
            0.9375f,  0.5f,     /* AO2 */
        };
        test_calib_write(test_uid_words, coef);
        run("pt.cal");
        test_calib_erase();
        run("pt.cal");
    }

    /* pt.calw: rewrites sector 15 with the record and keeps the rest of the
     * sector; refuses a bad record or a swapped bank before touching flash;
     * PortTool_S15Program refuses any word outside the sector. */
    {
        static const float coef[8] = { 1.0625f, -12.5f, 0.96875f, 0.125f,
                                       1.03125f, -0.25f, 0.9375f, 0.5f };
        static const uint32_t other_uid[3] = { 1u, 2u, 3u };
        uint8_t rec[sizeof(calib_area_t)], other[sizeof(calib_area_t)];
        char good[160], bad_crc[160], wrong_uid[160], not_hex[160], word[32];
        uintptr_t base = (uintptr_t)test_calib_area;
        unsigned erases;

        test_calib_write(other_uid, coef);
        memcpy(other, test_calib_area, sizeof(other));
        test_calib_write(test_uid_words, coef);
        memcpy(rec, test_calib_area, sizeof(rec));

        strcpy(good, "pt.calw ");
        strcpy(wrong_uid, "pt.calw ");
        for (unsigned i = 0; i < sizeof(rec); i++) {
            sprintf(good + strlen(good), "%02X", rec[i]);
            sprintf(wrong_uid + strlen(wrong_uid), "%02X", other[i]);
        }
        strcpy(bad_crc, good);
        bad_crc[strlen(bad_crc) - 1] = (bad_crc[strlen(bad_crc) - 1] == '0') ? '1' : '0';

        /* An older calibration and a root record the burn must keep. */
        test_calib_erase();
        memset(test_calib_area, 0x11, 64);
        memset(&test_calib_area[0x2000], 0x4F, 160);
        test_flash_erases = test_flash_programs = test_flash_stray = 0;

        erases = test_flash_erases;
        run(bad_crc);
        run(wrong_uid);
        run("pt.calw 4F43");
        run("pt.calw");
        strcpy(not_hex, good);
        not_hex[10] = 'G';
        run(not_hex);
        test_flash_regs.OPTCR = FLASH_OPTCR_SWAP_BANK;
        run(good);
        test_flash_regs.OPTCR = 0u;
        printf("TEST calw_refused_erases=%u\n", test_flash_erases - erases);

        run(good);
        printf("TEST calw_erases=%u programs=%u stray=%u record=%d tail_blank=%d root_kept=%d after_root_blank=%d\n",
               test_flash_erases, test_flash_programs, test_flash_stray,
               memcmp(test_calib_area, rec, sizeof(rec)) == 0,
               test_calib_area[sizeof(rec)] == 0xFFu && test_calib_area[CALIB_AREA_SIZE - 1u] == 0xFFu,
               test_calib_area[0x2000] == 0x4Fu && test_calib_area[0x2000 + 159] == 0x4Fu,
               test_calib_area[0x2000 + 160] == 0xFFu);

        test_flash_fail_erase = 1;
        run(good);
        test_flash_fail_erase = 0;

        test_calib_erase();
        memset(word, 0xA5, sizeof(word));
        test_flash_stray = 0;
        printf("TEST calw_guard below=%d above=%d misaligned=%d inside=%d stray=%u\n",
               PortTool_S15Program(base - 32u, (const uint8_t *)word),
               PortTool_S15Program(base + FLASH_SECTOR_SIZE, (const uint8_t *)word),
               PortTool_S15Program(base + 16u, (const uint8_t *)word),
               PortTool_S15Program(base + FLASH_SECTOR_SIZE - 32u, (const uint8_t *)word),
               test_flash_stray);
        test_calib_erase();
    }

    run("pt.list");

    /* Two sessions running side by side, which is the whole point of a
     * session. */
    run("pt.start din ch=1,3,5 period=200");
    run("pt.start relay ch=1,2 mode=cycle close=2000 open=2000 count=10");

    /* Below the 1000 ms floor: refused, not clamped (PORTTOOL-FLOW.md B.2). */
    run("pt.start relay close=100");
    run("pt.list");
    run("pt.caps");

    run("pt.set din ch=2,4");

    /* Refusals. A parameter that is present but malformed must take the whole
     * command down with a reason, never fall back to the previous value. */
    run("pt.start din ch=1,9");
    run("pt.start din period=abc");
    run("pt.start dinn");

    /* A kind=run port is not a session, so the session commands do not know
     * it. rtc: a port with no session to grow into. */
    run("pt.set rtc ch=1");

    run("pt.foo");

    /* ---- one-shot actions ----------------------------------------------
     *
     * pt.run has to come back, and its OK line has to survive the prose the
     * checks print on their way - see porttool_run.h. Sessions are left
     * running across it on purpose. */
    run("pt.run");
    run("pt.run sdram.probe");
    run("pt.run nosuch");

    /* A board whose FMC was never brought up: the reply still parses, and the
     * flags say what was wrong. No verdict, per DECISIONS.md 22. */
    test_sdram_ready = 0;
    run("pt.run sdram.probe");
    test_sdram_ready = 1;

    printf("TEST sdram_probe_count=%d\n", test_sdram_probe_count);

    /* sdram.crc: the window comes from the plan, and the reply has to say
     * which window it summed - a CRC without its range cannot be compared to
     * anything on the PC. The third call asks for a window past the end of the
     * array, which must be pulled back inside rather than wrapping. */
    run("pt.run sdram.crc");
    run("pt.run sdram.crc offset=1024 bytes=4096");
    /* Decimal: PortCmd_GetU32 takes digits only. */
    run("pt.run sdram.crc offset=2130706432 bytes=999999999");

    /* Why the board came up. The value is latched by main() before anything can
     * clear RCC->RSR; this target only puts it on the wire. Both the decoded
     * name and the raw register go out - the name's first-match order hides
     * flags when two causes are set at once. */
    run("pt.run reset.cause");
    test_reset_cause = "IWDG";
    test_reset_rsr = 0x20000000u;
    run("pt.run reset.cause");
    test_reset_cause = "PIN";
    test_reset_rsr = 0x04000000u;

    /* The RTC is not up in this image either, so the target has to bring it up
     * before it can read anything. */
    run("pt.run rtc.read");
    printf("TEST rtc_init_count=%d\n", test_rtc_init_count);

    /* A calendar nobody has ever set still has to answer, with init=0. */
    test_rtc_calendar_initialised = 0;
    run("pt.run rtc.read");
    test_rtc_calendar_initialised = 1;

    /* clk= has to follow RCC_BDCR. */
    test_rcc.BDCR = (2ul << RCC_BDCR_RTCSEL_Pos);   /* LSI */
    run("pt.run rtc.read");
    test_rcc.BDCR = (0ul << RCC_BDCR_RTCSEL_Pos);   /* no clock at all */
    run("pt.run rtc.read");
    test_rcc.BDCR = (1ul << RCC_BDCR_RTCSEL_Pos);   /* back to LSE */
    run("pt.run rtc.read");

    /* The two SD one-shots. A card that mounts and writes but reads back wrong
     * is a different fault from a card that was never there, and the reply has
     * to say which - a single pass/fail bit would not. */
    run("pt.run sd.probe");
    run("pt.run sd.integrity");

    test_sd_identical = 0;
    run("pt.run sd.integrity");
    test_sd_identical = 1;

    test_sd_detected = 0;
    run("pt.run sd.probe");
    run("pt.run sd.integrity");
    test_sd_detected = 1;

    /* ---- CAN session ----------------------------------------------------
     *
     * Internal loopback needs no bus and no peer, so the echo counter can be
     * driven here: the stub hands a sent frame straight back. What matters is
     * that listen mode does NOT transmit - bus monitoring exists to watch a
     * live bus without disturbing it, and a tool that injected frames while
     * claiming to be passive would be worse than no tool. */
    run("pt.stop all");
    run("pt.start can baud=250000 mode=loopback period=100");
    advance(100);
    advance(100);
    printf("TEST can_open=%d rate=%u mode=%lu sent=%lu\n",
           test_can_open_count, (unsigned)test_can_rate,
           (unsigned long)test_can_mode, (unsigned long)test_can_sent);

    run("pt.set can baud=500000");     /* refused: only at open */
    run("pt.set can mode=listen");     /* refused for the same reason */
    run("pt.set can period=200");      /* allowed */

    run("pt.stop can");
    test_can_sent = 0;
    run("pt.start can mode=listen period=100");
    advance(100);
    advance(100);
    printf("TEST can_listen_sent=%lu\n", (unsigned long)test_can_sent);
    run("pt.stop can");

    /* A controller that is not clocked looks exactly like a dead bus, so the
     * session refuses to start rather than reporting silence as a reading. */
    test_can_alive = 0;
    run("pt.start can");
    test_can_alive = 1;

    run("pt.start can baud=999");
    run("pt.start can mode=sideways");
    run("pt.stop all");

    /* mode=echo: the board answers instead of originating. Loopback is turned
     * off first, because a real bus does not hand a node its own frame back -
     * leaving it on would have the responder answering its own reply. */
    test_can_loopback = 0;
    test_can_sent = 0;
    run("pt.start can mode=echo period=100");
    advance(100);
    printf("TEST can_echo_idle_sent=%lu\n", (unsigned long)test_can_sent);
    {
        static const uint8_t probe[4] = { 0u, 0u, 0u, 41u };
        test_can_inject(0x123u, probe, 4u);
    }
    advance(100);
    printf("TEST can_echo_sent=%lu id=%lx len=%u payload=%u\n",
           (unsigned long)test_can_sent, (unsigned long)test_can_last_id,
           (unsigned)test_can_last_len, (unsigned)test_can_last_data[3]);
    run("pt.stop can");

    /* mode=bus, the CAN analyser window's (DECISIONS.md 111): nothing of its
     * own goes out, tx= sends exactly the frame asked for, and every frame
     * that arrives is reported - up to eight a pass, the rest counted. */
    test_can_sent = 0;
    run("pt.start can mode=bus period=100");
    advance(100);
    printf("TEST can_bus_idle_sent=%lu\n", (unsigned long)test_can_sent);
    run("pt.set can tx=123:1122AA");
    printf("TEST can_bus_sent=%lu id=%lx len=%u last=%u\n",
           (unsigned long)test_can_sent, (unsigned long)test_can_last_id,
           (unsigned)test_can_last_len, (unsigned)test_can_last_data[2]);
    run("pt.set can tx=7FF");
    run("pt.set can tx=800:11");       /* refused: not a standard ID */
    run("pt.set can tx=123:112");      /* refused: half a byte */
    run("pt.set can tx=123:112233445566778899"); /* refused: nine bytes */
    run("pt.set can tx=123:11 period=abc");      /* refused whole: nothing sent */
    printf("TEST can_bus_after_refusals=%lu\n", (unsigned long)test_can_sent);
    {
        static const uint8_t frame[2] = { 0xBEu, 0xEFu };
        for (int i = 0; i < 10; i++) {
            test_can_inject(0x456u, frame, 2u);
        }
    }
    advance(100);
    run("pt.stop can");
    run("pt.start can mode=normal");
    run("pt.set can tx=123:11");       /* refused: tx= is mode=bus only */
    run("pt.stop can");
    test_can_loopback = 1;
    run("pt.stop all");

    /* ---- ethernet session ------------------------------------------------
     *
     * The one session that needs a network stack, so the first thing worth
     * checking is that it brings lwIP up once and not once per start: the
     * generated MX_LWIP_Init registers a netif and starts DHCP, and calling it
     * twice would register a second one.
     *
     * Throughput is deliberately not checked here. The stub hands over a fixed
     * window and never blocks, so any rate it produced would be a property of
     * this file. What a host can check is the protocol: which port it listened
     * on, that the echo counter closes over the link and not over the control
     * port, and that a peer that never arrives leaves the counter stalled
     * rather than climbing. */
    run("pt.stop all");
    test_eth_set_link(1);
    run("pt.start eth mode=echo port=5000 period=100");
    test_eth_set_dhcp_address("192.168.1.50");
    printf("TEST eth_inits=%d listening=%d port=%u dhcp=%d\n",
           test_eth_lwip_inits, test_eth_listening,
           (unsigned)test_eth_bound_port, test_eth_dhcp_running);

    /* Nobody has connected: frames still go out, and the counter must stall. */
    advance(100);
    advance(100);
    printf("TEST eth_sent_before_peer=%lu\n", (unsigned long)test_eth_sent_len);

    /* The peer arrives and answers each number with the same number. */
    printf("TEST eth_connect=%d\n", test_eth_connect());
    advance(100);
    test_eth_feed("1\n");
    advance(100);
    test_eth_feed("2\n");
    advance(100);

    /* A second peer must be refused - one transfer at a time, or the byte
     * counts would be a mixture of two sessions. */
    printf("TEST eth_second_peer=%d\n", test_eth_connect());

    /* A reply to a frame that never existed is a miss, not a closed loop -
     * same contract as every other loop=link port. */
    test_eth_feed("99\n");
    advance(100);

    run("pt.echo eth 1");          /* a link port takes its echo off its link */

    /* sink counts what arrives without looking at it. */
    run("pt.stop eth");
    run("pt.start eth mode=sink port=5001 period=100");
    printf("TEST eth_rebound=%u\n", (unsigned)test_eth_bound_port);
    printf("TEST eth_sink_connect=%d\n", test_eth_connect());
    test_eth_feed("this is not a number and must not move the counter");
    advance(100);

    /* source pushes on its own with nobody asking. */
    run("pt.stop eth");
    test_eth_sent_len = 0; test_eth_sent[0] = '\0';
    run("pt.start eth mode=source port=5000 period=100");
    printf("TEST eth_source_connect=%d\n", test_eth_connect());
    advance(100);
    printf("TEST eth_source_pushed=%d\n", test_eth_sent_len > 0u);

    /* A static address is for a station with no DHCP server: the client has to
     * actually stop, or the lease would overwrite what the operator set. */
    run("pt.stop eth");
    run("pt.start eth ip=192.168.9.9");
    printf("TEST eth_static_dhcp=%d inits=%d\n",
           test_eth_dhcp_running, test_eth_lwip_inits);
    advance(1000);

    /* mirror sends back every byte unchanged, across more than one send
     * buffer's worth, and moves no counter (PORTTOOL-FLOW.md B.9). */
    run("pt.stop eth");
    {
        char blob[301];
        for (int i = 0; i < 300; i++) {
            blob[i] = (char)('A' + (i % 26));
        }
        blob[300] = '\0';
        test_eth_sent_len = 0; test_eth_sent[0] = '\0';
        run("pt.start eth mode=mirror port=5000 period=100");
        printf("TEST eth_mirror_connect=%d\n", test_eth_connect());
        test_eth_feed(blob);
        advance(100);
        advance(100);
        printf("TEST eth_mirror_same=%d len=%lu\n",
               (test_eth_sent_len == 300u) && (memcmp(test_eth_sent, blob, 300) == 0),
               (unsigned long)test_eth_sent_len);
    }

    run("pt.start eth mode=sideways");
    run("pt.start eth port=0");
    run("pt.start eth port=70000");
    run("pt.start eth ip=192.168.1");
    run("pt.stop all");

    /* ---- SD file transfer over eth mode=file ----------------------------
     *
     * PORTTOOL-FLOW.md B.11: the bytes ride the eth link, the card is the
     * in-memory folder in entries_stub.c. Written, acknowledged, read back,
     * listed, removed; a stalled transfer times out; a bad name is refused. */
    run("pt.start eth mode=file port=5000 period=100");
    printf("TEST sdf_connect=%d\n", test_eth_connect());
    test_eth_sent_len = 0; test_eth_sent[0] = '\0';
    run("pt.run sd.put name=hello.txt size=10");
    test_eth_feed("0123456789");
    advance(10);
    advance(10);   /* the fake send window opens every other pass */
    printf("TEST sdf_ack=%s", test_eth_sent);
    run("pt.run sd.ls");
    test_eth_sent_len = 0; test_eth_sent[0] = '\0';
    run("pt.run sd.get name=hello.txt");
    advance(10);
    advance(10);
    printf("TEST sdf_get_same=%d len=%lu\n",
           (test_eth_sent_len == 10u) && (memcmp(test_eth_sent, "0123456789", 10) == 0),
           (unsigned long)test_eth_sent_len);
    run("pt.run sd.put name=a/b size=1");
    run("pt.run sd.put name=slow.bin size=5");
    test_eth_feed("ab");
    advance(10);
    advance(11000);
    run("pt.run sd.rm name=hello.txt");
    run("pt.run sd.rm name=slow.bin");
    run("pt.run sd.ls");
    run("pt.run sd.eject");
    run("pt.stop all");

    /* ---- usb session -----------------------------------------------------
     *
     * The CDC pipe, driven as a data channel rather than as the bootloader's
     * upload path. Same three throughput modes as eth plus info, which moves
     * nothing and only reports enumeration.
     *
     * The state machine is what a host can check: a cable in but no
     * application at the other end reads identically to a dead link in every
     * counter, so the session has to report state= and it has to keep the
     * counter still in info mode - a mode nobody could answer must not report
     * misses. */
    run("pt.stop all");
    run("pt.start usb mode=echo period=100");
    printf("TEST usb_inits=%d\n", test_usb_inits);

    /* The PC answers each number with the same number. */
    advance(100);
    test_usb_feed("1\n");
    advance(100);
    test_usb_feed("2\n");
    advance(100);

    /* A reply to a frame that never existed is a miss, not a closed loop. */
    test_usb_feed("99\n");
    advance(100);

    run("pt.echo usb 1");        /* a link port takes its echo off its link */

    /* The host stopped reading: the endpoint refuses, and busy has to climb
     * while seq stalls - a different fault from an unplugged cable, and the
     * frame has to tell them apart. */
    test_usb_busy = 1;
    advance(100);
    advance(100);
    test_usb_busy = 0;

    /* Enumeration came and went. Counted every pass, not once per frame, so a
     * host that re-enumerates between two frames still leaves a trace. */
    test_usb_set_state(USBD_STATE_DEFAULT);
    advance(100);
    test_usb_set_state(USBD_STATE_CONFIGURED);
    advance(100);

    /* sink counts what arrives without looking at it. */
    run("pt.stop usb");
    test_usb_sent_len = 0; test_usb_sent[0] = '\0';
    run("pt.start usb mode=sink period=100");
    test_usb_feed("this is not a number and must not move the counter");
    advance(100);
    printf("TEST usb_sink_sent=%lu\n", (unsigned long)test_usb_sent_len);

    /* source pushes on its own with nobody asking. */
    run("pt.stop usb");
    test_usb_sent_len = 0; test_usb_sent[0] = '\0';
    run("pt.start usb mode=source period=100");
    advance(100);
    printf("TEST usb_source_pushed=%d\n", test_usb_sent_len > 0u);

    /* info moves nothing at all, and must not count misses for it. */
    run("pt.stop usb");
    test_usb_sent_len = 0; test_usb_sent[0] = '\0';
    run("pt.start usb mode=info period=100");
    advance(100);
    advance(100);
    advance(100);
    printf("TEST usb_info_sent=%lu inits=%d\n",
           (unsigned long)test_usb_sent_len, test_usb_inits);

    /* mirror sends back every byte unchanged; a burst bigger than its ring is
     * counted in drop= rather than silently lost (PORTTOOL-FLOW.md B.9). */
    run("pt.stop usb");
    {
        static char blob[3001];
        for (int i = 0; i < 3000; i++) {
            blob[i] = (char)('a' + (i % 26));
        }
        blob[200] = '\0';
        test_usb_sent_len = 0; test_usb_sent[0] = '\0';
        run("pt.start usb mode=mirror period=100");
        test_usb_feed(blob);
        advance(100);
        printf("TEST usb_mirror_same=%d len=%lu\n",
               (test_usb_sent_len == 200u) && (memcmp(test_usb_sent, blob, 200) == 0),
               (unsigned long)test_usb_sent_len);
        blob[200] = 'a' + (200 % 26);
        blob[3000] = '\0';
        test_usb_busy = 1;
        test_usb_feed(blob);
        advance(100);
        test_usb_busy = 0;
    }

    run("pt.start usb mode=sideways");
    run("pt.start usb period=abc");
    run("pt.stop all");

    /* ---- the deadman ----------------------------------------------------
     *
     * A timed run is timed by the PC, so the board has to survive the PC going
     * away mid-run. That is checked here rather than on a rack, because the
     * failure mode - outputs left driven by a run nobody is watching any more -
     * is exactly what nobody would notice on a rack.
     *
     * Renewal has to be explicit: the session frames below keep flowing while
     * the hold lapses, and a deadman that any traffic refreshed would never
     * fire on a PC that is still echoing from a dead panel. */
    run("pt.stop all");
    run("pt.hold");
    run("pt.hold abc");
    run("pt.hold 999999999");

    run("pt.start dout ch=1,2 duty=1:255,2:255 period=100");
    run("pt.start relay ch=1 mode=hold on=1:1");
    run("pt.hold 500");
    run("pt.hold");
    advance(100);
    printf("TEST hold_live_driving=%d dout_duty_any=%d\n",
           relays_energised(), dout_duty_any());

    /* Renewed once, so the original 500 ms deadline passes without firing. */
    run("pt.hold 500");
    advance(400);
    printf("TEST hold_renewed_driving=%d dout_duty_any=%d\n",
           relays_energised(), dout_duty_any());

    test_led_writes = 0;
    test_led_high = 0;
    advance(600);
    printf("TEST hold_expired_driving=%d dout_duty_any=%d led_high=%d\n",
           relays_energised(), dout_duty_any(), test_led_high);
    run("pt.list");
    run("pt.hold");

    /* Disarming leaves a session alone - it is the PC's clock that stopped
     * mattering, not the run. */
    run("pt.start dout ch=1 duty=1:255 period=100");
    run("pt.hold 500");
    run("pt.hold 0");
    advance(900);
    printf("TEST hold_disarmed_dout_duty_any=%d\n", dout_duty_any());
    run("pt.stop all");

    /* The indicator, driven from the PC rather than decided on the board. */
    test_led_writes = 0;
    test_led_high = 0;
    run("pt.led fault=1");
    printf("TEST led_fault_on=%d writes=%d\n", test_led_high, test_led_writes);
    /* Zeroed again: these are counters of writes, not the pin's level, so
     * "cleared it" means this command wrote the pin without writing it high. */
    test_led_writes = 0;
    test_led_high = 0;
    run("pt.led fault=0");
    printf("TEST led_fault_off=%d writes=%d\n", test_led_high, test_led_writes);
    run("pt.led fault=2");
    run("pt.led");

    /* Zeroed first: the checks above drive the same indicator, and the counters
     * are global. What this checks is what led.blink alone did. */
    test_led_writes = 0;
    test_led_high = 0;
    run("pt.run led.blink");
    printf("TEST led_configured=%d led_writes=%d led_high=%d\n",
           test_led_configured, test_led_writes, test_led_high);

    /* KNX diagnostics share the session's timers: idle they measure, with the
     * session running they answer busy=1 (PORTTOOL-FLOW.md A.3). */
    run("pt.stop all");
    run("pt.run knx.edges shots=2");
    run("pt.run knx.hist");
    run("pt.run knx.pullup");
    run("pt.start knx mode=listen period=1000");
    run("pt.run knx.hist");
    run("pt.stop knx");

    run("pt.stop all");
    run("pt.caps");

    /* ---- the card detect switch, and hot-plug ---------------------------
     *
     * The one thing about the SD slot that a person with the card in their
     * hand can check and nothing else can - except here, where the detect pin
     * is a variable. So this is where "an insertion is seen, and counted"
     * becomes decidable without a hand on the slot.
     *
     * A count and not just a level: a step that only ever sees detected=1
     * cannot tell a card that was put in during the test from one that was
     * already there when it started.
     */
    run("pt.stop all");
    test_sd_detected = 1;
    run("pt.start sd period=1000");
    advance(1000);                       /* a card, sitting there */

    test_sd_detected = 0;                /* pulled out */
    advance(10);
    advance(1000);

    test_sd_detected = 1;                /* put back */
    advance(10);
    advance(1000);

    /* Out and in again between two frames. The edge is sampled every
     * superloop pass rather than once per frame, so neither is lost. */
    test_sd_detected = 0; advance(10);
    test_sd_detected = 1; advance(10);
    advance(1000);

    /* Starting again zeroes the counters, or a plan step could be satisfied
     * by an insertion that happened before it began. */
    run("pt.start sd period=1000");
    advance(1000);

    run("pt.stop all");
    test_sd_detected = 1;

    /* ---- the four encoders ---------------------------------------------
     *
     * The same eight pins as the digital inputs, read as four A/B pairs. What
     * is decidable here and nowhere else is the decoding itself: which way a
     * transition counts, that the four pairs do not disturb each other, and
     * that a transition where both phases moved at once is reported as a miss
     * rather than guessed at. What is NOT decidable here is whether a real
     * encoder turns fast enough for this loop to keep up - that needs one.
     *
     * Every advance() is one pass of the superloop, so the pin state is
     * sampled once per call. The period is long enough that a frame only comes
     * out at the end of each sequence, which is what makes the count readable.
     */
    /* All eight channels selected on purpose: that is the widest this frame
     * ever gets, and the line-length check above is what it has to survive. */
    run("pt.start din ch=1,2,3,4,5,6,7,8 mode=quad period=1000");

    /* Encoder 1 is bit 0 (A) and bit 1 (B), so the state is (A<<1)|B and the
     * forward order 00 -> 01 -> 11 -> 10 is written 0x00, 0x02, 0x03, 0x01. */
    test_din_bits = 0x00u; advance(10);   /* the first sample primes, never counts */
    test_din_bits = 0x02u; advance(10);
    test_din_bits = 0x03u; advance(10);
    test_din_bits = 0x01u; advance(10);
    test_din_bits = 0x00u; advance(10);
    advance(1000);                        /* g1 must be +4, d1 +1, err 0 */

    /* Back the other way, over the same four states in reverse. The count has
     * to come back to zero: a decoder that counted turns instead of steps
     * would end at 8. */
    test_din_bits = 0x01u; advance(10);
    test_din_bits = 0x03u; advance(10);
    test_din_bits = 0x02u; advance(10);
    test_din_bits = 0x00u; advance(10);
    advance(1000);                        /* g1 back to 0, d1 now -1 */

    /* Both phases changing between two samples. There is no direction in it,
     * so it must land in err and leave the count alone. */
    test_din_bits = 0x03u; advance(10);
    advance(1000);                        /* err 1, g1 still 0 */

    /* Encoder 2 is bits 2 and 3 - the same pattern shifted up. Encoder 1 is
     * held still through it, so a decoder that mixed the pairs up would show
     * it moving. */
    test_din_bits = 0x00u; advance(10);
    test_din_bits = 0x08u; advance(10);
    test_din_bits = 0x0Cu; advance(10);
    test_din_bits = 0x04u; advance(10);
    test_din_bits = 0x00u; advance(10);
    advance(1000);                        /* g2 +4, g1 still 0 */

    /* And the level mode is unchanged by any of it. */
    run("pt.start din ch=1,2,3,4 mode=level period=1000");
    test_din_bits = 0x16u;
    advance(1000);

    run("pt.stop all");
    test_din_bits = 0x16u;

    /* ---- the echo loop -------------------------------------------------
     *
     * The board sends a number, the PC sends it back, the board counts on from
     * what it got. Three outcomes have to be distinguishable from the frames
     * alone, because that is all the panel gets: the loop closing, the loop
     * being dead, and a stale reply arriving for a frame that has passed. */
    run("pt.stop all");
    run("pt.start din ch=1 period=200");

    advance(200);                  /* first frame: nothing to have answered yet */
    run("pt.echo din 1");          /* the PC answers it */
    advance(200);                  /* seq moves to 2, miss back to 0 */
    run("pt.echo din 2");
    advance(200);                  /* seq 3 */

    advance(200);                  /* no answer this time */
    advance(200);                  /* still none: miss climbs, seq must not */

    run("pt.echo din 99");         /* an answer to a frame that never existed */
    advance(200);                  /* must count as a miss, not as closed */

    run("pt.echo relay 1");        /* relay is not running */
    run("pt.echo nosuch 1");
    run("pt.echo din notanumber");
    run("pt.echo can 1");          /* a link port takes its echo off its link */

    run("pt.stop all");

    /* ---- per-channel levels, and the promise that a refusal changes nothing --
     *
     * Six relays that can each hold their own level is the whole point of the
     * ch:value form; a set command that half-applies would be worse than one
     * that refuses, because nothing on screen would say which half took. */
    run("pt.start relay ch=1,2,3 mode=hold on=1:1,2:0,3:1");
    run("pt.caps");
    run("pt.set relay on=2:1");
    run("pt.caps");

    run("pt.start relay on=1:9");        /* level above 1 */
    run("pt.start relay on=7:1");        /* relay 7 does not exist */
    run("pt.start relay on=1:1,1:0");    /* the same relay twice */
    run("pt.start relay on=1:");         /* half an entry */
    run("pt.caps");                      /* none of those four may have landed */

    /* A line refused on its second parameter must not have applied its first. */
    run("pt.stop all");
    run("pt.start din ch=2,4 period=nonsense");
    run("pt.caps");

    /* ---- Digital Out: a duty per channel, and blink ---------------------
     *
     * Eight independent duties is the reason this is software PWM rather than
     * the timer channels, so "the duty landed on the channel that was named"
     * is the property worth pinning down. */
    run("pt.start dout ch=1,5 mode=hold duty=1:51,5:191 freq=1000");
    run("pt.caps");
    advance(1000);
    printf(">>> (host) dout duties now\n");
    printf("TEST dout_duty=");
    for (int i = 0; i < PORT_DOUT_COUNT; i++) {
        printf("%s%lu", i ? "," : "", (unsigned long)test_dout_duty[i]);
    }
    printf("\n");

    /* Blink is what makes a DI session on the other end of an eight-way cable
     * visibly follow, so the dark half really has to drive zero. */
    run("pt.start dout ch=1,2 mode=blink duty=255 period=200");
    advance(200);
    run("pt.caps");
    advance(200);
    run("pt.caps");

    /* ---- a frequency per channel ----------------------------------------
     *
     * Eight independent frequencies (port_dout.h). pt.caps reports the
     * frequency each channel really landed on, not the request. */
    run("pt.stop all");
    run("pt.start dout ch=1,2,3 mode=hold res=7 duty=64 freq=1:2000,2:1000,3:250");
    run("pt.caps");
    advance(1000);

    /* The widest spread, where the rounding is worst: must not report 0 Hz. */
    run("pt.set dout freq=1:2000,2:1");
    run("pt.caps");

    /* One value with no colon still means "all of them", so the two shapes of
     * the parameter do not diverge. */
    run("pt.set dout freq=500");
    run("pt.caps");
    advance(1000);

    /* ---- duty resolution in bits (DECISIONS.md 101) ---------------------
     *
     * The interrupt rate is capped, so the frequency limit moves with res, and
     * duty is a raw count that is not rescaled: values kept from before must
     * fit the new res, or the command is refused whole. */
    run("pt.set dout res=10");               /* keeps 500 Hz, over 250 Hz */
    run("pt.set dout res=10 freq=250");      /* keeps duty 64: fits, accepted */
    run("pt.set dout res=10 duty=1023");     /* full scale at 10 bits */
    run("pt.caps");
    advance(1000);
    run("pt.set dout res=4");                /* keeps duty 1023, over 15 */
    run("pt.set dout res=4 duty=15 freq=16000");  /* lower res, then higher freq */
    run("pt.caps");
    advance(1000);
    printf(">>> (host) dout after res=4\n");
    printf("TEST dout_res4_duty=");
    for (int i = 0; i < PORT_DOUT_COUNT; i++) {
        printf("%s%lu", i ? "," : "", (unsigned long)test_dout_duty[i]);
    }
    printf("\n");
    run("pt.set dout res=0");
    run("pt.set dout res=17");
    run("pt.set dout res=8 freq=1000 duty=255");
    run("pt.set dout freq=1001");            /* over 1000 Hz at 8 bits */

    run("pt.start dout duty=1:256");     /* over full scale at 8 bits */
    run("pt.start dout duty=9:50");      /* output 9 does not exist */
    run("pt.start dout freq=99999");     /* past what the timer will take */
    run("pt.start dout freq=1:99999");   /* same, in the per-channel form */
    run("pt.start dout freq=9:500");     /* output 9 does not exist */
    run("pt.start dout mode=sideways");

    /* ---- quadrature bursts (decision 107) --------------------------------
     *
     * The reference board feeds the board under test's DI encoders: four
     * pairs in step, B a quarter period ahead, exactly cycles*4 edges and
     * then all eight low. A pass that is late by many edges still moves one
     * edge only, so a slow loop stretches the burst instead of skipping
     * edges the far end would be blamed for. */
    run("pt.start dout mode=quad cycles=2 period=1000");
    {
        char seen[64] = "";
        for (int i = 0; i < 12; i++) {
            advance(i == 0 ? 100 : 3);
            int same = 1;
            for (int p = 1; p < 4; p++) {
                same &= (test_dout_duty[2 * p] != 0u) == (test_dout_duty[0] != 0u) &&
                        (test_dout_duty[2 * p + 1] != 0u) == (test_dout_duty[1] != 0u);
            }
            size_t n = strlen(seen);
            snprintf(seen + n, sizeof(seen) - n, "%s%d%d%s", i ? "," : "",
                     test_dout_duty[0] != 0u, test_dout_duty[1] != 0u, same ? "" : "!");
        }
        printf("TEST quad_ab=%s\n", seen);
    }
    run("pt.start dout mode=quad cycles=0");
    run("pt.start dout mode=quad cycles=10001");
    run("pt.stop dout");

    run("pt.stop all");
    printf(">>> (host) dout after pt.stop all\n");
    printf("TEST dout_after_stop=");
    for (int i = 0; i < PORT_DOUT_COUNT; i++) {
        printf("%s%lu", i ? "," : "", (unsigned long)test_dout_duty[i]);
    }
    printf(" stops=%d running=%d\n", test_dout_stop_count, test_dout_running);

    /* ---- analog: the reference gate ------------------------------------
     *
     * This board has no reference chip. VREF+ comes from VREFBUF inside the
     * MCU, and with it disabled the ADC still converts and still returns
     * numbers - values like 0x8000 that look exactly like real data. So the
     * property that matters is that the session refuses to start at all,
     * rather than starting and reporting plausible nonsense. */
    test_vref_fails = 1;
    run("pt.start ain");
    run("pt.start aout mv=500");
    run("pt.start temp");
    run("pt.list");                      /* none of them may be running */
    test_vref_fails = 0;

    run("pt.start ain period=500");
    run("pt.start temp period=1000");
    run("pt.start aout ch=1,2 mv=1:500,2:1234");
    run("pt.caps");
    advance(1000);
    printf(">>> (host) DAC outputs now\n");
    printf("TEST dac_mv=%lu,%lu vref_enables=%d\n",
           (unsigned long)test_dac_mv[0], (unsigned long)test_dac_mv[1],
           test_vref_enable_count);

    /* The XTR111 fault flags. Staged one high and one low, because a frame
     * that reported both channels the same would pass a firmware that read one
     * pin twice - and PI4/PE3 are different ports, which is exactly the kind
     * of pin map that gets copied wrong. */
    test_aout_ef[0] = 1;
    test_aout_ef[1] = 0;
    advance(1000);
    test_aout_ef[0] = 0;

    run("pt.start aout mv=9999");         /* past VREF+ */
    run("pt.start aout mv=1:4000");       /* same, per channel */
    run("pt.start ain ch=3");             /* only two analog inputs exist */

    /* Analog Out drives current into whatever is wired up, so stopping has to
     * put it back to zero rather than leave it where the session ended. */
    run("pt.stop all");
    printf(">>> (host) DAC after pt.stop all\n");
    printf("TEST dac_after_stop=%lu,%lu\n",
           (unsigned long)test_dac_mv[0], (unsigned long)test_dac_mv[1]);

    /* ---- RS485: the first loop=link port -------------------------------
     *
     * The echo travels over the pair being tested, so this counter really is
     * the verdict on that pair. The three states it has to tell apart are the
     * far end answering, the far end silent, and something on the pair that is
     * not an answer at all. */
    test_rs485_reset();
    run("pt.start rs485 baud=115200 period=200");
    advance(200);                        /* sends 1 on the pair */
    test_rs485_reply("1");               /* the far end answers */
    advance(200);                        /* seq -> 2, miss 0 */
    advance(200);                        /* silence: miss climbs, seq holds */
    test_rs485_reply("garbage");         /* something, but not an answer */
    advance(200);
    /* Terminators shown as | so this stays one line. Everything else in the
     * transcript is a board line, and a stray "2" on its own would break the
     * rule that every line starts with OK, ERR or !. */
    printf(">>> (host) what went out on the pair\n");
    printf("TEST rs485_sent=");
    for (const char *c = test_rs485_sent; *c != '\0'; c++) {
        putchar((*c == '\n' || *c == '\r') ? '|' : *c);
    }
    printf(" driving=%d baud=%lu\n",
           test_rs485_driving, (unsigned long)test_rs485_baud);

    /* pt.echo must be refused for a link port: answering on the control port
     * would advance the counter with the pair dead. */
    run("pt.echo rs485 2");

    run("pt.start rs485 baud=12345");

    /* pt.run rs485.pins while the session still holds PD4/PD5 as USART2's
     * alternate function. Re-muxing them here would leave the session running
     * and blaming the pair, so the target declines instead. */
    run("pt.run rs485.pins");
    run("pt.stop all");

    /* Now with nothing running: healthy pins follow what is written. */
    run("pt.run rs485.pins");

    /* And the fault it exists to find. GPIOD is bank 3; PD4 is the direction
     * pin, so this is a board where the transceiver can never be switched to
     * transmit - which, without this check, looks exactly like a pair with
     * nobody on the far end. */
    test_gpio_stuck_low[3] |= GPIO_PIN_4;
    run("pt.run rs485.pins");
    test_gpio_stuck_low[3] &= (uint16_t)~GPIO_PIN_4;

    /* ---- Relay mode=cycle: per-relay holds and counts (PORTTOOL-FLOW.md B.2) --
     *
     * Relay 1: 1 s open, 1 s closed, 2 cycles. Relay 2: 1 s open, 2 s closed,
     * 1 cycle. One frame at start and one per edge; done=1 once both reach
     * their count, and nothing moves after that. */
    run("pt.start relay ch=1,2 mode=cycle close=1:1000,2:2000 open=1000 count=1:2,2:1");
    advance(1);                          /* the session-start frame */
    advance(999);                        /* both close */
    advance(1000);                       /* 1 releases: n1=1 */
    advance(1000);                       /* 1 closes, 2 releases: n2=1, 2 done */
    advance(1000);                       /* 1 releases: n1=2, all done */
    advance(1000);                       /* nothing left to move: no frame */
    printf("TEST relay_cycle_energised=%d\n", relays_energised());
    run("pt.caps");
    run("pt.start relay mode=cycle close=999");
    run("pt.start relay mode=cycle open=1:500");
    run("pt.start relay mode=cycle count=0");
    run("pt.start relay mode=cycle count=1000001");
    run("pt.start relay mode=square");
    /* Six different values: over 192 bytes, within the 256 a PC line may take
     * (decision 123); caps then leaves close / open / count out. */
    run("pt.start relay ch=1,2,3,4,5,6 mode=cycle "
        "close=1:1000001,2:1000002,3:1000003,4:1000004,5:1000005,6:1000006 "
        "open=1:2000001,2:2000002,3:2000003,4:2000004,5:2000005,6:2000006 "
        "count=1:1000000,2:999999,3:999998,4:999997,5:999996,6:999995");
    run("pt.caps");
    run("pt.stop all");

    /* ---- RS232: loop=self, the one port whose counter IS the verdict ---- */
    run("pt.start rs232 period=200");
    advance(200);
    run("pt.echo rs232 1");              /* accepted: same wire, and that is the test */
    advance(200);
    run("pt.caps");
    run("pt.stop all");

    /* ---- RS232 at another rate: the whole control line switches (decision 104) */
    run("pt.start rs232 baud=19200 period=200");
    printf("TEST line_after_start uart4_baud=%lu\n", (unsigned long)test_uart4_baud);
    advance(200);                        /* nothing heard at 19200 yet: no frame */
    printf(">>> (host) a byte at the new rate\n");
    line_heard(fake_now);
    advance(200);
    run("pt.stop rs232");
    printf("TEST line_after_stop uart4_baud=%lu\n", (unsigned long)test_uart4_baud);
    run("pt.start rs232 baud=57600 period=200");
    advance(PORTTOOL_LINE_SILENCE_MS);   /* the PC went away */
    printf("TEST line_after_silence uart4_baud=%lu\n", (unsigned long)test_uart4_baud);
    run("pt.stop all");
    run("pt.start rs232 baud=4800");

    /* Stopping is responsible for putting the actuators back - the board has
     * no contact read-back, so this is the only place it can be checked. */
    printf(">>> (host) actuator state after pt.stop all\n");
    printf("TEST relay_energised=");
    for (int i = 0; i < RELAY_COUNT; i++) {
        printf("%d", test_relay_energised[i]);
    }
    printf("\n");
    printf("TEST relay_init_count=%d din_init_count=%d\n",
           test_relay_init_count, test_din_init_count);

    /* ---- A latched receive error must not deafen the command port -------
     *
     * Calls poll_command() rather than dispatch(). ISR is set by hand; what is
     * checked is that the firmware clears what it found and counts it. */
    printf(">>> (host) a latched receive error on the command port\n");
    test_uart_raise(USART_ISR_ORE);
    printf("TEST rx_before=%lu isr_raised=0x%lX\n",
           (unsigned long)PortTool_RxErrors(), (unsigned long)test_uart_isr());

    UART4_IRQHandler();
    poll_command();

    printf("TEST rx_after=%lu icr_written=0x%lX state=%d error_code=%lu\n",
           (unsigned long)PortTool_RxErrors(),
           (unsigned long)huart4.Instance->ICR,
           (int)huart4.RxState, (unsigned long)huart4.ErrorCode);

    /* ---- and the receive path end to end ------------------------------
     *
     * Feeds "pt.id\r" one byte at a time through the real ISR and then drains
     * the ring: the whole path a command takes on the board. */
    printf(">>> (host) a command delivered one byte at a time through the ISR\n");
    {
        const char *cmd = "pt.id\r";
        for (const char *c = cmd; *c != '\0'; c++) {
            test_uart_feed((uint8_t)*c);
        }
        printf("TEST fed=%u\n", (unsigned)strlen(cmd));
        poll_command();
    }

    return 0;
}
