/*
 * The CAN hardware layer, stubbed.
 *
 * The real port_can.c is not compiled here: it is FDCAN register work behind a
 * private HAL copy, and the harness has no FDCAN headers. What the contract
 * test needs from CAN is the session's behaviour - what it refuses, what its
 * frame says, whether it stops transmitting in listen mode - and that is all
 * decided in porttool_can.c above this line.
 *
 * The loopback here is the interesting part: a frame handed to PortCan_Send is
 * handed straight back by PortCan_Receive, so the echo counter can be driven
 * without a bus.
 */

#include "port_can.h"

#include <stdio.h>
#include <string.h>

int      test_can_open_count;
int      test_can_close_count;
int      test_can_alive = 1;
int      test_can_open_fails;
uint8_t  test_can_rate;
uint32_t test_can_mode;
uint32_t test_can_sent;          /* how many frames the session handed over */
uint32_t test_can_tec, test_can_rec;

/* The last frame handed to PortCan_Send, so mode=echo can be checked on what
 * it actually put back on the wire and not only on how many times it did. */
uint32_t test_can_last_id;
uint8_t  test_can_last_data[8];
uint8_t  test_can_last_len;

/* One frame of loopback, which is all a tick ever needs. */
static uint8_t  q_data[8];
static uint8_t  q_len;
static uint32_t q_id;
static int      q_full;
static int      s_open;

/* Set to 0 to model a bus that swallows everything - a dead transceiver. */
int test_can_loopback = 1;

static const uint32_t RATES[PORT_CAN_RATE_COUNT] = {
    125000u, 250000u, 500000u, 1000000u
};

uint32_t PortCan_RateBps(uint8_t index)
{
    return (index < PORT_CAN_RATE_COUNT) ? RATES[index] : 0u;
}

int PortCan_RateIndex(uint32_t bps, uint8_t *out_index)
{
    for (uint8_t i = 0; i < PORT_CAN_RATE_COUNT; i++) {
        if (RATES[i] == bps) {
            if (out_index != NULL) { *out_index = i; }
            return 1;
        }
    }
    return 0;
}

int PortCan_Timing(uint8_t index, uint16_t *prescaler, uint16_t *seg1,
                   uint16_t *seg2, uint16_t *sjw)
{
    if (index >= PORT_CAN_RATE_COUNT) { return 0; }
    if (prescaler != NULL) { *prescaler = 1u; }
    if (seg1 != NULL)      { *seg1 = 43u; }
    if (seg2 != NULL)      { *seg2 = 6u; }
    if (sjw != NULL)       { *sjw = 4u; }
    return 1;
}

uint32_t PortCan_Dlc(uint8_t len) { return len; }

int PortCan_Init(void) { return 1; }

int PortCan_Open(uint8_t rate_index, uint32_t mode, int auto_retx)
{
    (void)auto_retx;
    if (test_can_open_fails) { return 0; }
    test_can_open_count++;
    test_can_rate = rate_index;
    test_can_mode = mode;
    q_full = 0;
    s_open = 1;
    return 1;
}

void PortCan_Close(void)
{
    if (s_open) { test_can_close_count++; }
    s_open = 0;
    q_full = 0;
}

/* Puts one frame where PortCan_Receive will find it, without anything having
 * been sent first. mode=echo originates nothing, so loopback cannot drive it -
 * the responder needs a frame that arrives from somewhere else. */
void test_can_inject(uint32_t id, const uint8_t *data, uint8_t len)
{
    q_id = id;
    q_len = len;
    memcpy(q_data, data, (len > 8u) ? 8u : len);
    q_full = 1;
}

int PortCan_Send(uint32_t id, const uint8_t *data, uint8_t len)
{
    if (!s_open) { return 0; }
    test_can_sent++;
    test_can_last_id = id;
    test_can_last_len = len;
    memcpy(test_can_last_data, data, (len > 8u) ? 8u : len);
    if (test_can_loopback) {
        q_id = id;
        q_len = len;
        memcpy(q_data, data, (len > 8u) ? 8u : len);
        q_full = 1;
    }
    return 1;
}

int PortCan_Receive(uint32_t *id, uint8_t *data, uint8_t *len)
{
    if (!s_open || !q_full) { return 0; }
    q_full = 0;
    if (id != NULL)   { *id = q_id; }
    if (len != NULL)  { *len = q_len; }
    if (data != NULL) { memcpy(data, q_data, 8); }
    return 1;
}

void PortCan_Counters(uint32_t *tec, uint32_t *rec)
{
    if (tec != NULL) { *tec = test_can_tec; }
    if (rec != NULL) { *rec = test_can_rec; }
}

uint32_t PortCan_LastError(void) { return 0u; }
int PortCan_Alive(void) { return test_can_alive; }
uint32_t PortCan_ClockHz(void) { return 25000000u; }
const char *PortCan_ClockName(void) { return "HSE"; }
