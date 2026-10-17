/*
 * Stubs for the board-side measurements the port tool sessions call: they
 * stand in for the measurement, and test_* let a test stage the failures.
 * Why the rig is built this way: $PROD/docs/engineering/TEST-DESIGN.md.
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "ETH/eth_test.h"
#include "SD/sd_test.h"
#include "SDRAM/sdram_test.h"

/* test_sd_*: a missing card, or a card that reads back wrong. */
int test_sd_detected = 1;
int test_sd_ready = 1;
int test_sd_identical = 1;

/* The detect switch: a test moves test_sd_detected between advance() calls to
 * stage an insertion or a removal. */
int SD_Test_Detected(void)
{
    return test_sd_detected ? 1 : 0;
}

void SD_Test_Probe(sd_probe_t *out)
{
    printf("SDCARD_TEST: probing (stub)\r\n");

    out->detected = (uint8_t)(test_sd_detected ? 1 : 0);
    out->ready = (uint8_t)((test_sd_detected && test_sd_ready) ? 1 : 0);
    out->block_count  = out->ready ? 62333952u : 0u;
    out->block_size   = out->ready ? 512u : 0u;
    out->capacity_mib = out->ready ? 30436u : 0u;
    out->card_type    = 1u;
    out->version_2x   = (uint8_t)(out->ready ? 1 : 0);
    out->card_class   = out->ready ? 1461u : 0u;
    /* FAT32: the simulated board stands for a correctly prepared one. */
    out->fs_type      = out->ready ? SD_FS_FAT32 : SD_FS_NONE;
    out->hal_error    = 0u;
}

int SD_Test_IntegrityOnce(uint32_t bytes, sd_integrity_t *out)
{
    printf("SDCARD_TEST: one integrity round (stub)\r\n");

    /* Echoes bytes=, so a test sees it reached the target. */
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

/* The Ethernet PHY over MDIO: test_eth_* stage no PHY, no link, or a link. */
int test_eth_present = 1;
int test_eth_link = 1;

int ETH_Test_Probe(eth_probe_t *out)
{
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

/* The stress pass. test_sd_stress_fail_at is 0 for a clean run, or the 1-based
 * round that goes wrong. */
int test_sd_stress_fail_at = 0;

#define TEST_SD_STRESS_PASSES 64u

/* Fixed timings, not a real clock. */
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
    printf("SDCARD_TEST: stress (stub)\r\n");

    out->bytes_each = (bytes != 0u) ? bytes : 4096u;
    out->passes = (passes != 0u) ? passes : TEST_SD_STRESS_PASSES;
    out->passed = 0;
    out->bytes_total = 0;
    out->elapsed_ms = 1234u;
    out->first_bad_pass = 0;
    out->fresult = 0;

    if (!test_sd_detected || !test_sd_ready) {
        /* As the real one: nothing attempted means passes 0. */
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

/* test_sdram_*: what the board "found", including an FMC never brought up. */
int test_sdram_ready = 1;
int test_sdram_databus_ok = 1;
int test_sdram_addrbus_ok = 1;
int test_sdram_probe_count = 0;

void SDRAM_Test_Probe(sdram_probe_t *out)
{
    test_sdram_probe_count++;

    /* Prints like the real one, so pt.run's OK line is tested against it. */
    printf("SDRAM_TEST: probing (stub)\r\n");

    out->base = 0xC0000000UL;
    out->size_bytes = 0x04000000UL;
    out->ready = (uint8_t)(test_sdram_ready ? 1 : 0);
    out->databus_ok = (uint8_t)((test_sdram_ready && test_sdram_databus_ok) ? 1 : 0);
    out->addrbus_ok = (uint8_t)((test_sdram_ready && test_sdram_addrbus_ok) ? 1 : 0);
}

/* test_sdram_sweep_mismatches: the array has bad words; the sweep finishes. */
int test_sdram_sweep_mismatches = 0;

int SDRAM_Test_SweepOnce(sdram_sweep_t *out)
{
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

/* The two halves of a retention cycle: only the bookkeeping the session builds
 * its frame from; test_sdram_retention_failed drives the verify half. */
void SDRAM_Test_RetentionWrite(uint32_t *rng_state, sdram_retention_t *out)
{
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
    out->failed = (uint32_t)test_sdram_retention_failed;
    out->first_bad_addr = (out->failed > 0u) ? 0xC0A0B000UL : 0u;
}

/* Only the window clamping is real: a window off the end is pulled back
 * inside, and the reply describes the window it summed. */
int SDRAM_Test_Crc32Once(uint32_t offset, uint32_t length, sdram_crc_t *out)
{
    const uint32_t size = 0x04000000UL;

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

    /* Derived from the window, so two windows give two numbers. */
    out->crc = 0xC0FFEE00UL ^ offset ^ (length << 1);
    return 1;
}

/* The reset cause main() latched on a board; a test sets it here. */
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

/* ---- SD file access: an in-memory folder ------------------------------
 *
 * Stands in for SD_File_* (sd_test.c) so the transfer state machine in
 * porttool_sdfile.c runs end to end on the host. test_sd_detected = 0 makes
 * every call fail the way a missing card does (-1). */

#define TEST_SDF_FILES 16
typedef struct {
    char     name[72];
    uint8_t *data;
    uint32_t size;
    int      used;
} test_sdf_file_t;

static test_sdf_file_t test_sdf[TEST_SDF_FILES];
static int      test_sdf_open = -1;
static int      test_sdf_writing;
static uint32_t test_sdf_pos;

static int test_sdf_find(const char *name)
{
    for (int i = 0; i < TEST_SDF_FILES; i++) {
        if (test_sdf[i].used && strcmp(test_sdf[i].name, name) == 0) {
            return i;
        }
    }
    return -1;
}

int SD_File_Close(void)
{
    test_sdf_open = -1;
    return 0;
}

int SD_File_OpenWrite(const char *name)
{
    int i;

    if (!test_sd_detected) {
        return -1;
    }
    i = test_sdf_find(name);
    if (i < 0) {
        for (i = 0; i < TEST_SDF_FILES && test_sdf[i].used; i++) { }
        if (i == TEST_SDF_FILES) {
            return 7;   /* FR_DENIED */
        }
        test_sdf[i].used = 1;
        snprintf(test_sdf[i].name, sizeof(test_sdf[i].name), "%s", name);
    }
    free(test_sdf[i].data);
    test_sdf[i].data = NULL;
    test_sdf[i].size = 0;
    test_sdf_open = i;
    test_sdf_writing = 1;
    return 0;
}

int SD_File_OpenRead(const char *name, uint32_t *size)
{
    int i;

    if (!test_sd_detected) {
        return -1;
    }
    i = test_sdf_find(name);
    if (i < 0) {
        return 4;   /* FR_NO_FILE */
    }
    test_sdf_open = i;
    test_sdf_writing = 0;
    test_sdf_pos = 0;
    if (size != NULL) {
        *size = test_sdf[i].size;
    }
    return 0;
}

int SD_File_Write(const uint8_t *buf, uint32_t n)
{
    test_sdf_file_t *f;
    uint8_t *grown;

    if (test_sdf_open < 0 || !test_sdf_writing) {
        return 9;   /* FR_INVALID_OBJECT */
    }
    f = &test_sdf[test_sdf_open];
    grown = (uint8_t *)realloc(f->data, f->size + n + 1u);
    if (grown == NULL) {
        return 7;
    }
    f->data = grown;
    memcpy(f->data + f->size, buf, n);
    f->size += n;
    return 0;
}

int SD_File_Read(uint8_t *buf, uint32_t n, uint32_t *got)
{
    test_sdf_file_t *f;
    uint32_t left;

    if (test_sdf_open < 0 || test_sdf_writing) {
        return 9;
    }
    f = &test_sdf[test_sdf_open];
    left = f->size - test_sdf_pos;
    if (n > left) {
        n = left;
    }
    if (n) {
        memcpy(buf, f->data + test_sdf_pos, n);
    }
    test_sdf_pos += n;
    if (got != NULL) {
        *got = n;
    }
    return 0;
}

int SD_File_List(sd_file_each_t each, void *ctx, uint32_t *count)
{
    uint32_t n = 0;

    if (!test_sd_detected) {
        return -1;
    }
    for (int i = 0; i < TEST_SDF_FILES; i++) {
        if (!test_sdf[i].used) {
            continue;
        }
        n++;
        if (each != NULL) {
            /* 2026-10-13 12:34, so the date fields are checked as parsed. */
            each(test_sdf[i].name, test_sdf[i].size,
                 (uint16_t)(((2026u - 1980u) << 9) | (10u << 5) | 13u),
                 (uint16_t)((12u << 11) | (34u << 5)), ctx);
        }
    }
    if (count != NULL) {
        *count = n;
    }
    return 0;
}

int SD_File_Remove(const char *name)
{
    int i;

    if (!test_sd_detected) {
        return -1;
    }
    i = test_sdf_find(name);
    if (i < 0) {
        return 4;
    }
    free(test_sdf[i].data);
    memset(&test_sdf[i], 0, sizeof(test_sdf[i]));
    return 0;
}

int SD_File_Eject(void)
{
    test_sdf_open = -1;
    return 0;
}
