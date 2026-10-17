/*
 * The CAN hardware layer, stubbed (port_can.c needs the FDCAN HAL). A frame
 * handed to PortCan_Send comes straight back from PortCan_Receive, so the echo
 * counter runs without a bus.
 */

#include "port_can.h"

#include <stdio.h>
#include <string.h>

uint32_t HAL_GetTick(void);       /* hal_stub.c */
void     test_set_tick(uint32_t ms);

int      test_can_open_count;
int      test_can_alive = 1;
int      test_can_open_fails;
uint8_t  test_can_rate;
uint32_t test_can_mode;
uint32_t test_can_sent;          /* how many frames the session handed over */
uint32_t test_can_tec, test_can_rec;

/* The last frame handed to PortCan_Send, so mode=echo is checked on content. */
uint32_t test_can_last_id;
uint8_t  test_can_last_data[8];
uint8_t  test_can_last_len;

/* What PortCan_Receive hands out next: a looped-back or injected frame. A few
 * deep: the simulated board injects faster than one tick drains. */
#define Q_DEPTH 16u
static struct { uint32_t id; uint8_t len; uint8_t data[8]; } q[Q_DEPTH];
static uint32_t q_head, q_count;
static int      s_open;

int test_can_state;   /* PORT_CAN_STATE_*, what PortCan_State reports */

static void q_put(uint32_t id, const uint8_t *data, uint8_t len)
{
    uint32_t at;

    if (q_count == Q_DEPTH) {   /* a full FIFO drops the newest, as FDCAN's does */
        return;
    }
    at = (q_head + q_count) % Q_DEPTH;
    q[at].id = id;
    q[at].len = (len > 8u) ? 8u : len;
    memset(q[at].data, 0, 8);
    memcpy(q[at].data, data, q[at].len);
    q_count++;
}

/* Set to 0 to model a bus that swallows everything - a dead transceiver. */
int test_can_loopback = 1;
/* sim.cut can: nothing comes back, and each empty receive moves the tick on
 * 1 ms, since the sim's tick otherwise stands still inside a receive wait. */
int test_can_silent;

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
    q_count = 0;
    s_open = 1;
    return 1;
}

void PortCan_Close(void)
{
    s_open = 0;
    q_count = 0;
}

/* Puts one frame where PortCan_Receive will find it, for mode=echo, which
 * originates nothing. */
void test_can_inject(uint32_t id, const uint8_t *data, uint8_t len)
{
    q_put(id, data, len);
}

int PortCan_Send(uint32_t id, const uint8_t *data, uint8_t len)
{
    if (!s_open) { return 0; }
    test_can_sent++;
    test_can_last_id = id;
    test_can_last_len = len;
    memcpy(test_can_last_data, data, (len > 8u) ? 8u : len);
    if (test_can_loopback && !test_can_silent) {
        q_put(id, data, len);
    }
    return 1;
}

int PortCan_Receive(uint32_t *id, uint8_t *data, uint8_t *len)
{
    if (test_can_silent) {
        test_set_tick(HAL_GetTick() + 1u);
    }
    if (!s_open || q_count == 0u) { return 0; }
    if (id != NULL)   { *id = q[q_head].id; }
    if (len != NULL)  { *len = q[q_head].len; }
    if (data != NULL) { memcpy(data, q[q_head].data, 8); }
    q_head = (q_head + 1u) % Q_DEPTH;
    q_count--;
    return 1;
}

void PortCan_Counters(uint32_t *tec, uint32_t *rec)
{
    if (tec != NULL) { *tec = test_can_tec; }
    if (rec != NULL) { *rec = test_can_rec; }
}

uint32_t PortCan_LastError(void) { return 0u; }
int PortCan_State(void) { return test_can_state; }
int PortCan_Alive(void) { return test_can_alive; }
uint32_t PortCan_ClockHz(void) { return 25000000u; }
const char *PortCan_ClockName(void) { return "HSE"; }
