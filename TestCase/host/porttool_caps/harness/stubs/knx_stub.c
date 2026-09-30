/*
 * The KNX session entry points, stubbed.
 *
 * The real ones live in knx_test.c behind two timing ISRs and a TIM1 input
 * capture; none of that exists on a host. What the contract test decides is
 * the session's behaviour above that line: that listen mode never transmits,
 * that a character coming back closes the loop, that a wrong byte is counted
 * as a mismatch rather than a pass, and that the frame says which.
 *
 * The loopback here is one character deep, which is all a tick uses.
 */

#include "KNX/knx_test.h"

#include <stdio.h>

int test_knx_init_count;
int test_knx_sent;              /* how many characters the session put out */
uint8_t test_knx_last_sent;

/* Set to 0 for a bus that swallows everything, or corrupt to model a byte that
 * comes back wrong - the two failures a KNX session has to tell apart. */
int test_knx_loopback = 1;
int test_knx_corrupt;
int test_knx_framing_bad;

int test_knx_bus = 0;           /* 0 ok, 1 dead, 2 odd */
uint32_t test_knx_pulses;

static int      q_full;
static uint8_t  q_byte;

int KNX_Test_SessionInit(void)
{
    test_knx_init_count++;
    printf("KNX_TEST: capture and bit engine started (stub)\r\n");
    q_full = 0;
    return 1;
}

void KNX_Test_SessionStatsReset(void)
{
    q_full = 0;
    test_knx_pulses = 0;
}

int KNX_Test_SessionSendChar(uint8_t b)
{
    test_knx_sent++;
    test_knx_last_sent = b;
    test_knx_pulses += 4u;      /* a character is a handful of active pulses */
    if (test_knx_loopback) {
        q_byte = test_knx_corrupt ? (uint8_t)(b ^ 0xFFu) : b;
        q_full = 1;
    }
    return 1;
}

int KNX_Test_SessionPollChar(uint8_t *out, uint8_t *framing_ok)
{
    if (!q_full) {
        return 0;
    }
    q_full = 0;
    if (out != NULL)        { *out = q_byte; }
    if (framing_ok != NULL) { *framing_ok = (uint8_t)(test_knx_framing_bad ? 0 : 1); }
    return 1;
}

/* --- Frame layer -------------------------------------------------------- */

/* One frame deep, which is all a tick uses. test_knx_frame_which lets a test
 * pick which reading of the octets is meant to pass, because the whole point
 * of crc= is that the session reports that answer rather than assuming one. */
int      test_knx_frames_sent;
uint8_t  test_knx_last_frame[KNX_FRAME_MAX];
uint8_t  test_knx_last_frame_len;
int      test_knx_frame_which = KNX_FRAME_CRC_RAW;
int      test_knx_bus_idle = 1;
int      test_knx_ack_after_frame = 1;   /* model a peer that acknowledges */
uint32_t test_knx_partials;

static uint8_t f_pending;        /* 0 none, 1 the frame, 2 the ack octet */

void KNX_Test_SessionFrameReset(void)
{
    f_pending = 0;
    test_knx_partials = 0;
}

uint8_t KNX_Test_AckKind(uint8_t octet)
{
    if ((octet & 0x33u) != 0x00u)                            { return KNX_ACK_NONE; }
    if (((octet & 0x0Cu) != 0u) && ((octet & 0xC0u) != 0u))  { return KNX_ACK_ACK; }
    if ((octet & 0xC0u) == 0u)                               { return KNX_ACK_NAK; }
    return KNX_ACK_BUSY;
}

int KNX_Test_SessionPollFrame(uint8_t *out, uint8_t *out_len, uint8_t cap,
                              uint8_t *which, uint8_t *bad_chars)
{
    if (bad_chars != NULL) { *bad_chars = 0; }

    if (f_pending == 1u) {
        uint8_t n = test_knx_last_frame_len;
        f_pending = test_knx_ack_after_frame ? 2u : 0u;
        if (n > cap) { n = cap; }
        for (uint8_t i = 0; i < n; i++) { out[i] = test_knx_last_frame[i]; }
        if (out_len != NULL) { *out_len = n; }
        if (which != NULL)   { *which = (uint8_t)test_knx_frame_which; }
        return 1;
    }
    if (f_pending == 2u) {
        f_pending = 0;
        out[0] = 0xCCu;                    /* L_Ack ACK */
        if (out_len != NULL) { *out_len = 1u; }
        if (which != NULL)   { *which = KNX_FRAME_CRC_BAD; }
        return 1;
    }
    return 0;
}

uint32_t KNX_Test_SessionPartialFrames(void)
{
    return test_knx_partials;
}

uint8_t KNX_Test_SessionSendGroupWrite(uint16_t src, uint16_t ga, uint8_t value,
                                       uint8_t *out, uint8_t cap,
                                       uint8_t *bus_was_idle)
{
    uint8_t f[9];
    uint8_t x = 0u;

    if (cap < sizeof(f)) {
        return 0u;
    }
    /* The same layout the real builder produces, so a test reading the octets
     * is reading what a board would have sent. */
    f[0] = 0xBCu;
    f[1] = (uint8_t)(src >> 8);
    f[2] = (uint8_t)(src & 0xFFu);
    f[3] = (uint8_t)(ga >> 8);
    f[4] = (uint8_t)(ga & 0xFFu);
    f[5] = 0xE1u;
    f[6] = 0x00u;
    f[7] = (uint8_t)(0x80u | (value & 0x3Fu));
    for (uint8_t i = 0; i < 8u; i++) { x ^= f[i]; }
    f[8] = (uint8_t)~x;

    for (uint8_t i = 0; i < sizeof(f); i++) {
        out[i] = f[i];
        test_knx_last_frame[i] = f[i];
    }
    test_knx_last_frame_len = (uint8_t)sizeof(f);
    test_knx_frames_sent++;
    test_knx_pulses += 40u;
    if (bus_was_idle != NULL) { *bus_was_idle = (uint8_t)(test_knx_bus_idle ? 1 : 0); }
    if (test_knx_loopback) { f_pending = 1u; }
    return (uint8_t)sizeof(f);
}

void KNX_Test_SessionStats(knx_session_stats_t *out)
{
    if (out == NULL) {
        return;
    }
    out->bus     = (uint8_t)test_knx_bus;
    out->vcc_ok  = (uint8_t)((test_knx_bus == 0) ? 1 : 0);
    out->bus_ok  = (uint8_t)((test_knx_bus == 0) ? 1 : 0);
    out->rx_idle = (uint8_t)((test_knx_bus == 1) ? 1 : 0);

    out->pulses  = test_knx_pulses;
    out->dropped = 0;

    out->w_min = 33; out->w_max = 37; out->w_avg = 35; out->w_count = test_knx_pulses;
    out->d_min = 5;  out->d_max = 9;  out->d_avg = 7;  out->d_count = test_knx_pulses;

    out->ms_since_edge = 12;
}
