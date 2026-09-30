/*
 * Fake Digital Out hardware.
 *
 * Records the duty last pushed at each of the eight channels, so the test can
 * check the things the session shell decides: which channels a command
 * touched, that a per-channel duty landed on the right channel, that blink
 * really drives zero on its dark half, and that stopping puts every 24 V
 * output back to zero.
 *
 * *** The real port_dout.c is NOT compiled here. Its interrupt handler and
 * *** prescaler arithmetic need the HAL timer module and the RCC registers,
 * *** and what could go wrong with them - edge timing under load, what the
 * *** VNQ5160K-E actually reproduces - is not decidable on a PC anyway. That
 * *** part is covered by a scope on the terminal, not by this harness.
 */

#include "port_dout.h"

#include <stddef.h>

/* Test-visible state. */
uint32_t test_dout_duty[PORT_DOUT_COUNT];
int      test_dout_init_count;
int      test_dout_stop_count;
uint32_t test_dout_freq_hz[PORT_DOUT_COUNT];
int      test_dout_running;

const port_dout_t port_dout_pins[PORT_DOUT_COUNT] = {
    { NULL, 0u, "A03" }, { NULL, 0u, "A04" },
    { NULL, 0u, "A05" }, { NULL, 0u, "A06" },
    { NULL, 0u, "A07" }, { NULL, 0u, "A08" },
    { NULL, 0u, "A09" }, { NULL, 0u, "A10" },
};

int PortDout_Init(void)
{
    for (int i = 0; i < PORT_DOUT_COUNT; i++) {
        test_dout_freq_hz[i] = PORT_DOUT_FREQ_DEF_HZ;
    }
    test_dout_init_count++;
    test_dout_running = 1;
    return 1;
}

/* Clamps the way the real one does, so a session that hands over an
 * out-of-range frequency is judged on what the hardware would have produced
 * rather than on what it asked for. */
int PortDout_SetFreq(int ch, uint32_t freq_hz)
{
    if (ch < 1 || ch > PORT_DOUT_COUNT) {
        return 0;
    }
    if (freq_hz < PORT_DOUT_FREQ_MIN_HZ) { freq_hz = PORT_DOUT_FREQ_MIN_HZ; }
    if (freq_hz > PORT_DOUT_FREQ_MAX_HZ) { freq_hz = PORT_DOUT_FREQ_MAX_HZ; }
    test_dout_freq_hz[ch - 1] = freq_hz;
    return 1;
}

void PortDout_SetDuty(int ch, uint32_t duty_pct)
{
    if (ch < 1 || ch > PORT_DOUT_COUNT) {
        return;
    }
    if (duty_pct > 100u) {
        duty_pct = 100u;
    }
    test_dout_duty[ch - 1] = duty_pct;
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

/* Runs the real driver's rounding rather than handing the request straight
 * back. It is pure integer arithmetic with no hardware in it, and it is where
 * the off-by-one that shipped on 2026-09-10 would have been caught: a stub
 * that echoes the request cannot fail the way the board did. */
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

/* The real driver's interrupt rate is the fastest channel times the duty
 * resolution. Reproduced rather than faked, so the transcript shows a number
 * that moves with the frequencies the test sets. */
uint32_t PortDout_TickHz(void)
{
    uint32_t top = PORT_DOUT_FREQ_MIN_HZ;
    for (int i = 0; i < PORT_DOUT_COUNT; i++) {
        if (test_dout_freq_hz[i] > top) { top = test_dout_freq_hz[i]; }
    }
    return top * PORT_DOUT_STEPS;
}
