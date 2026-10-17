/*
 * Fake analog hardware: VREFBUF, the two ADC inputs, the two temperature
 * sensors and the two DAC outputs. It tests the gate (a session refuses to
 * start without the reference; test_vref_fails makes PortVref_Enable() fail),
 * not the converters: the real port_adc.c / port_dac.c / port_vref.c need the
 * HAL and are not compiled here.
 */

#include "port_adc.h"
#include "port_dac.h"
#include "port_vref.h"

/* Test-visible knobs and state. */
int      test_vref_fails;
int      test_vref_enable_count;
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
    return 1;
}

uint32_t PortAdc_VddaMv(void)      { return test_vdda_mv; }
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
    return 1;
}

uint32_t PortDac_VrefMv(void) { return 2500u; }

int PortDac_SetMv(int ch, uint32_t mv)
{
    if (ch < 1 || ch > PORT_AOUT_COUNT) {
        return 0;
    }
    test_dac_mv[ch - 1] = mv;
    return 1;
}

/* 12 bits over a 2500 mV span (VREFBUF): the real quantisation, so the frame's
 * asked/quantised pair differs the way it will on the board. */
uint32_t PortDac_QuantisedMv(uint32_t mv)
{
    uint32_t code = (mv * 4095u) / 2500u;
    return (code * 2500u) / 4095u;
}

/* Iout = Vin * 10 / Rset, in microamps. */
uint32_t PortDac_ExpectedMicroamps(uint32_t mv)
{
    return (PortDac_QuantisedMv(mv) * 10000u) / PORT_XTR111_RSET_OHM;
}

/* The XTR111 fault pins, set by a test; the frame must carry the level
 * unchanged (port_dac.h). */
int test_aout_ef[2] = {0, 0};

int PortDac_FaultLevel(int ch)
{
    if (ch < 1 || ch > 2) {
        return 0;
    }
    return test_aout_ef[ch - 1] ? 1 : 0;
}
