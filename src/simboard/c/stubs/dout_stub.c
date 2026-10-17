/*
 * Fake Digital Out hardware: records the duty last pushed at each channel.
 * The real port_dout.c needs the HAL timer and RCC and is not compiled here.
 */

#include "port_dout.h"

#include <stddef.h>

/* Test-visible state. */
uint32_t test_dout_duty[PORT_DOUT_COUNT];
int      test_dout_stop_count;
uint32_t test_dout_freq_hz[PORT_DOUT_COUNT];
int      test_dout_running;
uint32_t test_dout_res = PORT_DOUT_RES_DEF;

const port_dout_t port_dout_pins[PORT_DOUT_COUNT] = {
    { NULL, 0u }, { NULL, 0u }, { NULL, 0u }, { NULL, 0u },
    { NULL, 0u }, { NULL, 0u }, { NULL, 0u }, { NULL, 0u },
};

int PortDout_Init(void)
{
    for (int i = 0; i < PORT_DOUT_COUNT; i++) {
        test_dout_freq_hz[i] = PORT_DOUT_FREQ_DEF_HZ;
    }
    test_dout_running = 1;
    return 1;
}

/* Clamps the way the real one does. */
int PortDout_SetFreq(int ch, uint32_t freq_hz)
{
    if (ch < 1 || ch > PORT_DOUT_COUNT) {
        return 0;
    }
    if (freq_hz < PORT_DOUT_FREQ_MIN_HZ) { freq_hz = PORT_DOUT_FREQ_MIN_HZ; }
    if (freq_hz > PORT_DOUT_FREQ_MAX_HZ(test_dout_res)) {
        freq_hz = PORT_DOUT_FREQ_MAX_HZ(test_dout_res);
    }
    test_dout_freq_hz[ch - 1] = freq_hz;
    return 1;
}

/* Refuses and parks the way the real one does. */
int PortDout_SetRes(uint32_t res)
{
    if (res < PORT_DOUT_RES_MIN || res > PORT_DOUT_RES_MAX) {
        return 0;
    }
    if (res == test_dout_res) {
        return 1;
    }
    for (int i = 0; i < PORT_DOUT_COUNT; i++) {
        if (test_dout_freq_hz[i] > PORT_DOUT_FREQ_MAX_HZ(res)) {
            return 0;
        }
    }
    test_dout_res = res;
    PortDout_AllOff();
    return 1;
}

uint32_t PortDout_Res(void) { return test_dout_res; }

void PortDout_SetDuty(int ch, uint32_t duty)
{
    if (ch < 1 || ch > PORT_DOUT_COUNT) {
        return;
    }
    uint32_t full = (1u << test_dout_res) - 1u;
    if (duty > full) {
        duty = full;
    }
    test_dout_duty[ch - 1] = duty;
}

void PortDout_AllOff(void)
{
    for (int i = 0; i < PORT_DOUT_COUNT; i++) {
        test_dout_duty[i] = 0u;
    }
}

void PortDout_Stop(void)
{
    test_dout_stop_count++;
    test_dout_running = 0;
    PortDout_AllOff();
}

/* The real driver's rounding (pure arithmetic), not the request echoed back. */
uint32_t PortDout_ActualFreqHz(int ch)
{
    if (ch < 1 || ch > PORT_DOUT_COUNT) {
        return 0;
    }
    uint32_t tick = PortDout_TickHz();
    uint64_t num  = ((uint64_t)test_dout_freq_hz[ch - 1] << 32) + (tick / 2u);
    uint32_t inc  = (uint32_t)(num / tick);
    if (inc == 0u) { inc = 1u; }
    return (uint32_t)((((uint64_t)inc * tick) + 0x80000000ull) >> 32);
}

/* The real driver's interrupt rate: the fastest channel times 2^res. */
uint32_t PortDout_TickHz(void)
{
    uint32_t top = PORT_DOUT_FREQ_MIN_HZ;
    for (int i = 0; i < PORT_DOUT_COUNT; i++) {
        if (test_dout_freq_hz[i] > top) { top = test_dout_freq_hz[i]; }
    }
    return top << test_dout_res;
}
