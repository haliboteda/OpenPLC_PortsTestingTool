/*
 * Fake analog hardware: the reference buffer, the two ADC inputs, the two
 * temperature sensors and the two DAC outputs.
 *
 * What this exists to test is the gate, not the converters. This board has no
 * reference chip - VREF+ comes from VREFBUF inside the MCU - and with it
 * disabled the ADC still converts and still returns numbers that look exactly
 * like real data. So the property worth pinning down is that a session refuses
 * to start at all when the reference is not up, rather than starting and
 * reporting plausible nonsense.
 *
 * test_vref_fails lets the test make PortVref_Enable() say no.
 *
 * *** The real port_adc.c / port_dac.c / port_vref.c are NOT compiled here.
 * *** Their conversion setup and the millivolt arithmetic need the HAL ADC and
 * *** DAC modules, and what can actually go wrong with them - whether the
 * *** analog jumpers JP1-JP9 were ever soldered, what the XTR111 really puts
 * *** out - is measured with a meter on the terminal, not decided on a PC.
 */

#include "port_adc.h"
#include "port_dac.h"
#include "port_vref.h"

/* Test-visible knobs and state. */
int      test_vref_fails;
int      test_vref_enable_count;
int      test_adc_init_count;
int      test_dac_init_count;
uint32_t test_dac_mv[PORT_AOUT_COUNT];
int      test_vdda_trusted = 1;
uint32_t test_vdda_mv = 3287u;

/* Readings, writable so the simulated board can present a good unit while the
 * transcript test keeps the fixed values its golden file was written against.
 * Two clearly different analog inputs, so a channel mix-up is visible. */
uint32_t test_ain_raw[PORT_AIN_COUNT]  = { 21850u, 530u };
uint32_t test_ain_mv[PORT_AIN_COUNT]   = { 2071u, 50u };
uint32_t test_temp_mv[PORT_TEMP_COUNT] = { 738u, 751u };

/* ---- port_vref.c -------------------------------------------------------- */

int PortVref_Enable(void)
{
    test_vref_enable_count++;
    return test_vref_fails ? 0 : 1;
}

/* ---- port_adc.c --------------------------------------------------------- */

int PortAdc_Init(void)
{
    test_adc_init_count++;
    return 1;
}

uint32_t PortAdc_VddaMv(void)      { return test_vdda_mv; }
uint32_t PortAdc_VrefintRaw(void)  { return 22100u; }
int      PortAdc_VddaMeasured(void) { return 1; }
int      PortAdc_VddaTrusted(void) { return test_vdda_trusted; }

int PortAdc_ReadAin(int ch, uint32_t *raw, uint32_t *mv)
{
    if (ch < 1 || ch > PORT_AIN_COUNT) {
        return 0;
    }
    *raw = test_ain_raw[ch - 1];
    *mv  = test_ain_mv[ch - 1];
    return 1;
}

int PortAdc_ReadAin1SwitchClosed(uint32_t *raw)
{
    *raw = 18300u;
    return 1;
}

int PortAdc_ReadTemp(int ch, uint32_t *mv, int32_t *decic)
{
    if (ch < 1 || ch > PORT_TEMP_COUNT) {
        return 0;
    }
    *mv = test_temp_mv[ch - 1];
    *decic = (int32_t)(*mv) - PORT_TEMP_OFFSET_MV;   /* 10 mV per degC */
    return 1;
}

/* ---- port_dac.c --------------------------------------------------------- */

int PortDac_Init(void)
{
    test_dac_init_count++;
    return 1;
}

uint32_t PortDac_VrefMv(void) { return 3300u; }

int PortDac_SetMv(int ch, uint32_t mv)
{
    if (ch < 1 || ch > PORT_AOUT_COUNT) {
        return 0;
    }
    test_dac_mv[ch - 1] = mv;
    return 1;
}

/* 12 bits over a 3300 mV span: the real quantisation, so the frame's
 * asked/quantised pair differs the way it will on the board. */
uint32_t PortDac_QuantisedMv(uint32_t mv)
{
    uint32_t code = (mv * 4095u) / 3300u;
    return (code * 3300u) / 4095u;
}

/* Iout = Vin * 10 / Rset, in microamps. */
uint32_t PortDac_ExpectedMicroamps(uint32_t mv)
{
    return (PortDac_QuantisedMv(mv) * 10000u) / PORT_XTR111_RSET_OHM;
}

/* The XTR111 fault flags. A test drives these to stage a fault, which is the
 * only way that path is exercisable off a board - and the point of staging it
 * is that the frame has to carry the pin level unchanged, whichever level a
 * test chooses. Nothing here decides which level means fault; neither does the
 * firmware (see port_dac.h). */
int test_aout_ef[2] = {0, 0};

int PortDac_FaultLevel(int ch)
{
    if (ch < 1 || ch > 2) {
        return 0;
    }
    return test_aout_ef[ch - 1] ? 1 : 0;
}
