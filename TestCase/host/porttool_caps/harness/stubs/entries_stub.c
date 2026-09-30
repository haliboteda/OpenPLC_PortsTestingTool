/*
 * Stubs for the board-side measurements the port tool sessions call.
 *
 * SD and SDRAM one-shots stand in for the measurement instead of failing the
 * run, so a test can model a missing card and a card that reads back wrong --
 * two failures that look alike from a distance. The detect switch is polled
 * every superloop pass, so moving it between advance() calls is the only way
 * hot-plug is decidable without a hand on the slot.
 *
 * 2026-09-16: the fourteen standalone bring-up entries were removed. They
 * existed for porttool_handover.c's function-pointer table, which went with
 * pt.handover; nothing in the harness source list named them any more, and
 * the build links clean without them.
 *
 * Why the rig is built this way: $PROD/docs/engineering/TEST-DESIGN.md.
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "KNX/knx_test.h"
#include "CAN/can_test.h"
#include "RS485/rs485_test.h"
#include "PWM/pwm_test.h"
#include "RS232/rs232_test.h"
#include "ETH/eth_test.h"
#include "SD/sd_test.h"
#include "SDRAM/sdram_test.h"
#include "bringup_test.h"


/* The SD one-shots, like the SDRAM probe below: pt.run calls them and comes
 * back, so they stand in for the measurement instead of failing the run.
 * test_sd_* let a test model a missing card and a card that reads back wrong -
 * the two failures that look alike from a distance. */
int test_sd_detected = 1;
int test_sd_ready = 1;
int test_sd_identical = 1;
int test_sd_probe_count = 0;
int test_sd_integrity_count = 0;

/* The detect switch on its own. The session polls this every superloop pass,
 * so a test moves test_sd_detected between advance() calls to stage an
 * insertion or a removal - which is the only way hot-plug is decidable
 * without a hand on the card slot. */
int SD_Test_Detected(void)
{
    return test_sd_detected ? 1 : 0;
}

void SD_Test_Probe(sd_probe_t *out)
{
    test_sd_probe_count++;
    printf("SDCARD_TEST: probing (stub)\r\n");

    out->detected = (uint8_t)(test_sd_detected ? 1 : 0);
    out->ready = (uint8_t)((test_sd_detected && test_sd_ready) ? 1 : 0);
    out->block_count  = out->ready ? 62333952u : 0u;
    out->block_size   = out->ready ? 512u : 0u;
    out->capacity_mib = out->ready ? 30436u : 0u;
    out->card_type    = 1u;
    out->version_2x   = (uint8_t)(out->ready ? 1 : 0);
    out->card_class   = out->ready ? 1461u : 0u;
    /* The simulated board stands for a correctly prepared one, and station 6
     * requires FAT32 - an exFAT card is a real failure the plan is meant to
     * catch, so the fixture must not be the thing producing it. */
    out->fs_type      = out->ready ? SD_FS_FAT32 : SD_FS_NONE;
    out->hal_error    = 0u;
}

int SD_Test_IntegrityOnce(uint32_t bytes, sd_integrity_t *out)
{
    test_sd_integrity_count++;
    printf("SDCARD_TEST: one integrity round (stub)\r\n");

    /* Echo back what was asked for, so the contract test can assert that a
     * plan's bytes= actually reached the target rather than being dropped on
     * the way. */
    out->bytes = (bytes != 0u) ? bytes : 4096u;
    out->fresult = 0;
    if (!test_sd_detected || !test_sd_ready) {
        out->mounted = 0; out->wrote = 0; out->read_back = 0; out->identical = 0;
        out->write_crc = 0; out->read_crc = 0; out->fresult = -1;
        return 0;
    }
    out->mounted = 1; out->wrote = 1; out->read_back = 1;
    out->write_crc = 0xDEADBEEFu;
    out->read_crc = test_sd_identical ? 0xDEADBEEFu : 0x11112222u;
    out->identical = (uint8_t)(test_sd_identical ? 1 : 0);
    return test_sd_identical ? 1 : 0;
}

/* The Ethernet PHY over MDIO. test_eth_* let a test model the three answers a
 * station has to tell apart: no PHY at all (a board fault), a PHY with no link
 * (usually an unplugged cable), and a negotiated link. */
int test_eth_present = 1;
int test_eth_link = 1;
int test_eth_probe_count = 0;

int ETH_Test_Probe(eth_probe_t *out)
{
    test_eth_probe_count++;
    printf("ETH_TEST: MDIO bring-up (stub)\r\n");

    memset(out, 0, sizeof(*out));
    out->mdio_ready = 1;
    if (!test_eth_present) {
        return 0;
    }
    out->found  = 1;
    out->addr   = 0;
    out->phy_id = 0x0007C131u;      /* LAN8742A, as the board really reports */
    out->bcr    = 0x1000u;
    if (test_eth_link) {
        out->bsr          = 0x782Du;
        out->scsr         = 0x1058u;
        out->link         = 1;
        out->autoneg_done = 1;
        out->speed_mbit   = 100;
        out->full_duplex  = 1;
    } else {
        out->bsr  = 0x7809u;        /* no link, negotiation not complete */
        out->scsr = 0x0040u;
    }
    return 1;
}

void ETH_Test_Run(void)
{
    printf("ETH_TEST: watch (stub) - would not return\r\n");
    for (;;) { }
}

/* The stress pass. test_sd_stress_fail_at is 0 for a clean run, or the 1-based
 * round that goes wrong - which is how a test checks that a run stopping early
 * is reported as passes< attempted rather than as a smaller clean result. */
int test_sd_stress_fail_at = 0;
int test_sd_stress_count = 0;

#define TEST_SD_STRESS_PASSES 64u

/* Rates a test can steer. Fixed millisecond counts rather than a real clock:
 * the contract test asserts that bytes= reached the target and that the fields
 * come out in the frame, not how fast this PC is. */
uint32_t test_sd_write_ms = 2000u;
uint32_t test_sd_read_ms  = 1000u;

int SD_Test_Speed(uint32_t bytes, sd_speed_t *out)
{
    if (out == NULL) {
        return 0;
    }
    memset(out, 0, sizeof(*out));
    out->bytes = (bytes != 0u) ? bytes : 4096u;

    if (!test_sd_detected || !test_sd_ready) {
        out->fresult = -1;
        return 0;
    }
    out->mounted   = 1;
    out->write_ms  = test_sd_write_ms;
    out->read_ms   = test_sd_read_ms;
    out->write_bps = (out->write_ms != 0u)
                   ? (uint32_t)(((uint64_t)out->bytes * 1000ull) / out->write_ms) : 0u;
    out->read_bps  = (out->read_ms != 0u)
                   ? (uint32_t)(((uint64_t)out->bytes * 1000ull) / out->read_ms) : 0u;
    printf("SDCARD_TEST: speed (stub)\r\n");
    return 1;
}

int SD_Test_StressOnce(uint32_t bytes, uint32_t passes, sd_stress_t *out)
{
    test_sd_stress_count++;
    printf("SDCARD_TEST: stress (stub)\r\n");

    out->bytes_each = (bytes != 0u) ? bytes : 4096u;
    out->passes = (passes != 0u) ? passes : TEST_SD_STRESS_PASSES;
    out->passed = 0;
    out->bytes_total = 0;
    out->elapsed_ms = 1234u;
    out->first_bad_pass = 0;
    out->fresult = 0;

    if (!test_sd_detected || !test_sd_ready) {
        /* Mirrors the real one: nothing attempted means passes 0, not the 64
         * that were planned. */
        out->mounted = 0;
        out->passes  = 0;
        out->fresult = -1;
        return 0;
    }
    out->mounted = 1;

    if (test_sd_stress_fail_at > 0) {
        out->passed = (uint32_t)(test_sd_stress_fail_at - 1);
        out->passes = (uint32_t)test_sd_stress_fail_at;
        out->first_bad_pass = (uint32_t)test_sd_stress_fail_at;
        out->bytes_total = out->passed * out->bytes_each;
        return 0;
    }

    out->passed = out->passes;
    out->bytes_total = out->passed * out->bytes_each;
    return 1;
}

/* SDRAM_Test_Probe is the opposite case: pt.run is supposed to call it and
 * come back, so it stands in for the measurement instead of failing the run.
 * test_sdram_* let a test decide what the board "found", including the failure
 * shape - a controller that was never brought up. */
int test_sdram_ready = 1;
int test_sdram_databus_ok = 1;
int test_sdram_addrbus_ok = 1;
int test_sdram_probe_count = 0;

void SDRAM_Test_Probe(sdram_probe_t *out)
{
    test_sdram_probe_count++;

    /* The real one prints as it goes, and pt.run has to keep that prose out of
     * the middle of its OK line. Printing here is what makes that testable. */
    printf("SDRAM_TEST: probing (stub)\r\n");

    out->base = 0xC0000000UL;
    out->size_bytes = 0x04000000UL;
    out->ready = (uint8_t)(test_sdram_ready ? 1 : 0);
    out->databus_ok = (uint8_t)((test_sdram_ready && test_sdram_databus_ok) ? 1 : 0);
    out->addrbus_ok = (uint8_t)((test_sdram_ready && test_sdram_addrbus_ok) ? 1 : 0);
}

/* test_sdram_sweep_mismatches is how a test says "the array has a bad word":
 * the sweep still finishes, and the count is the whole finding. */
int test_sdram_sweep_mismatches = 0;
int test_sdram_sweep_count = 0;

int SDRAM_Test_SweepOnce(sdram_sweep_t *out)
{
    test_sdram_sweep_count++;
    printf("SDRAM_TEST: full sweep (stub)\r\n");

    memset(out, 0, sizeof(*out));
    out->ready = (uint8_t)(test_sdram_ready ? 1 : 0);
    if (!test_sdram_ready) {
        return 0;
    }

    out->patterns = 4u;
    out->words_each = 0x04000000UL / 4u;
    out->write_ms = 8000u;
    out->verify_ms = 6000u;
    out->mismatches = (uint32_t)test_sdram_sweep_mismatches;
    if (out->mismatches > 0u) {
        out->first_bad_offset = 0x00A0B000UL;
        out->first_bad_pattern = 0x55555555UL;
        return 0;
    }
    return 1;
}

int test_sdram_retention_failed = 0;
int test_sdram_retention_count = 0;

int SDRAM_Test_RetentionOnce(sdram_retention_t *out)
{
    test_sdram_retention_count++;
    printf("SDRAM_TEST: retention cycle (stub)\r\n");

    memset(out, 0, sizeof(*out));
    out->ready = (uint8_t)(test_sdram_ready ? 1 : 0);
    if (!test_sdram_ready) {
        return 0;
    }

    out->checked = 64u;
    out->wait_ms = 5000u;
    out->seed = 0x12345678UL;
    out->failed = (uint32_t)test_sdram_retention_failed;
    if (out->failed > 0u) {
        out->first_bad_addr = 0xC0A0B000UL;
        return 0;
    }
    return 1;
}

/* The two halves of a retention cycle, for the session that waits between
 * them on its own clock. The harness has no 64 MiB array to write into, so
 * what it stands in for is the bookkeeping the session builds its frame from -
 * and test_sdram_retention_failed drives the verify half, which is how the
 * "failed is summed over the whole run, not just the last cycle" behaviour
 * gets to be seen failing rather than only passing. */
int test_sdram_retention_write_count = 0;
int test_sdram_retention_verify_count = 0;

void SDRAM_Test_RetentionWrite(uint32_t *rng_state, sdram_retention_t *out)
{
    test_sdram_retention_write_count++;

    if (*rng_state == 0u) { *rng_state = 1u; }
    *rng_state ^= *rng_state << 13;

    out->seed = *rng_state;
    out->checked = 64u;
    out->failed = 0u;
    out->first_bad_addr = 0u;
    out->wait_ms = 5000u;
    out->ready = (uint8_t)(test_sdram_ready ? 1 : 0);
}

void SDRAM_Test_RetentionVerify(sdram_retention_t *out)
{
    test_sdram_retention_verify_count++;

    out->failed = (uint32_t)test_sdram_retention_failed;
    out->first_bad_addr = (out->failed > 0u) ? 0xC0A0B000UL : 0u;
}

/* The clamping is the part worth standing in for. The real one sums bytes out
 * of a 64 MiB mapping, which the harness has no business allocating; what a
 * contract test can check is that a window the plan asked for came back
 * described - and that a window running off the end was pulled back inside
 * rather than wrapping into a CRC that looks like an answer. */
int test_sdram_crc_count = 0;

int SDRAM_Test_Crc32Once(uint32_t offset, uint32_t length, sdram_crc_t *out)
{
    const uint32_t size = 0x04000000UL;

    test_sdram_crc_count++;
    printf("SDRAM_TEST: crc window (stub)\r\n");

    memset(out, 0, sizeof(*out));

    if (offset >= size) { offset = 0u; }
    if (length == 0u || length > (size - offset)) { length = size - offset; }

    out->offset = offset;
    out->length = length;
    out->ready = (uint8_t)(test_sdram_ready ? 1 : 0);
    if (!test_sdram_ready) {
        return 0;
    }

    /* Derived from the window rather than fixed, so a test that asked for two
     * different windows and got one number would be caught. */
    out->crc = 0xC0FFEE00UL ^ offset ^ (length << 1);
    return 1;
}

/* The reset cause. On a board main() latches RCC->RSR before anything can
 * clear it; here there is no such register, so a test says what the board
 * "came up from" and checks that the target reports it rather than deciding
 * anything about it. */
uint32_t test_reset_rsr = 0x04000000u;          /* PINRSTF, a plain reset */
const char *test_reset_cause = "PIN";

uint32_t boot_handoff_reset_rsr(void)
{
    return test_reset_rsr;
}

const char *boot_handoff_reset_cause_str(void)
{
    return test_reset_cause;
}
