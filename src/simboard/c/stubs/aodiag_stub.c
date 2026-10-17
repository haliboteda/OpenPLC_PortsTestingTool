/*
 * *** TEMPORARY: fake hardware for pt.aodiag; remove together with it. ***
 * Documented in $PROD/maps/fixture-test-stages/issues/STG-41-where-does-the-ao-noise-come-from.md
 *
 * Every wait returns at once, so the sim exercises the command path only.
 * The real port_aodiag.c (interrupt masking, TIM2, WFI, DAC/VREFBUF
 * registers) is NOT compiled here.
 */

#include "port_aodiag.h"

#include <stddef.h>

uint32_t test_aodiag_quiet_secs;
uint32_t test_aodiag_vref_scale;
int      test_aodiag_buffer_on[2] = { 1, 1 };
int      test_aodiag_dac_off[2];

uint32_t PortAodiag_QuietSleep(uint32_t secs)
{
    test_aodiag_quiet_secs = secs;
    return secs * 1000u;
}

/* Runs step three times so the E4 / E5 bodies execute once each way. */
uint32_t PortAodiag_Spin(uint32_t ms, uint32_t step_ms, void (*step)(void))
{
    uint32_t n = 0u;

    (void)ms; (void)step_ms;
    if (step != NULL) {
        for (; n < 3u; n++) { step(); }
    }
    return n;
}

int PortAodiag_DacBuffer(int ch, int on)
{
    if (ch < 1 || ch > 2) { return 0; }
    test_aodiag_buffer_on[ch - 1] = on;
    return 1;
}

int PortAodiag_DacOff(int ch)
{
    if (ch < 1 || ch > 2) { return 0; }
    test_aodiag_dac_off[ch - 1] = 1;
    return 1;
}

int PortAodiag_VrefScale(uint32_t scale)
{
    if (scale > 3u) { return 0; }
    test_aodiag_vref_scale = scale;
    return 1;
}
