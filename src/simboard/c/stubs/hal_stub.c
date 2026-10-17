/*
 * Fake hardware for the port tool host test: the clock, UART4, flash sector
 * 15, GPIO, relays, DI, LED and RTC. The relay stub remembers its state so a
 * test can check that pt.stop releases every relay.
 */

#include "main.h"
#include "relay.h"
#include "port_din.h"
#include "calib_area.h"

#include <stddef.h>
#include <string.h>

uint32_t test_uid_words[3] = { 0x00340041u, 0x31355114u, 0x39303538u };

/* Sector 15; its start is the calibration area pt.cal reads. Records are built
 * through calib_area_t so a change to the real header changes what the sim
 * board reports. */
uint8_t test_calib_area[FLASH_SECTOR_SIZE] __attribute__((aligned(32)));

FLASH_TypeDef test_flash_regs;
unsigned test_flash_erases, test_flash_programs, test_flash_stray;
int test_flash_fail_erase;

HAL_StatusTypeDef HAL_FLASH_Unlock(void) { return HAL_OK; }
HAL_StatusTypeDef HAL_FLASH_Lock(void) { return HAL_OK; }
uint32_t HAL_FLASH_GetError(void) { return test_flash_fail_erase ? 0x100u : 0u; }

HAL_StatusTypeDef HAL_FLASHEx_Erase(FLASH_EraseInitTypeDef *e, uint32_t *sector_error)
{
    *sector_error = 0xFFFFFFFFu;
    if (test_flash_fail_erase) {
        return HAL_ERROR;
    }
    /* Only sector 15 exists here; any other sector is a stray erase. */
    if (e->TypeErase != FLASH_TYPEERASE_SECTORS || e->Banks != FLASH_BANK_2
        || e->Sector != FLASH_SECTOR_7 || e->NbSectors != 1u) {
        test_flash_stray++;
        return HAL_ERROR;
    }
    test_flash_erases++;
    memset(test_calib_area, 0xFF, sizeof(test_calib_area));
    return HAL_OK;
}

HAL_StatusTypeDef test_flash_program(uint32_t type, uintptr_t dst, uintptr_t src)
{
    uintptr_t base = (uintptr_t)test_calib_area;

    if (type != FLASH_TYPEPROGRAM_FLASHWORD || dst < base
        || dst - base > sizeof(test_calib_area) - 32u || (dst - base) % 32u != 0u) {
        test_flash_stray++;
        return HAL_ERROR;
    }
    for (unsigned i = 0; i < 32u; i++) {
        if (test_calib_area[dst - base + i] != 0xFFu) {
            return HAL_ERROR;   /* a word is written once per erase */
        }
    }
    memcpy(&test_calib_area[dst - base], (const void *)src, 32u);
    test_flash_programs++;
    return HAL_OK;
}

void test_calib_erase(void)
{
    memset(test_calib_area, 0xFF, sizeof(test_calib_area));
}

/* CRC-32 (IEEE 802.3, the zlib one), bitwise: what calib_area.h specifies. */
static uint32_t test_crc32(const uint8_t *p, size_t n)
{
    uint32_t c = 0xFFFFFFFFu;
    while (n--) {
        c ^= *p++;
        for (int k = 0; k < 8; k++) {
            c = (c >> 1) ^ (0xEDB88320u & (0u - (c & 1u)));
        }
    }
    return ~c;
}

void test_calib_write(const uint32_t uid[3], const float gain_offset[8])
{
    calib_area_t a;

    memset(&a, 0, sizeof(a));
    a.magic    = CALIB_MAGIC;
    a.version  = CALIB_VERSION;
    a.channels = CALIB_CHANNELS;
    memcpy(a.uid, uid, sizeof(a.uid));
    for (unsigned i = 0; i < CALIB_CHANNELS; i++) {
        a.ch[i].gain   = gain_offset[2u * i];
        a.ch[i].offset = gain_offset[2u * i + 1u];
    }
    a.crc32 = test_crc32((const uint8_t *)&a, offsetof(calib_area_t, crc32));

    test_calib_erase();
    memcpy(test_calib_area, &a, sizeof(a));
}

GPIO_TypeDef test_gpio_banks[11];

/* huart4's register block. A test checks that the firmware writes ICR to clear
 * a receive error. */
static USART_TypeDef fake_uart4_regs;
UART_HandleTypeDef huart4 = { &fake_uart4_regs, 0, HAL_UART_STATE_READY };
USART_TypeDef *const UART4 = &fake_uart4_regs;

void HAL_NVIC_SetPriority(test_irq_t irq, uint32_t pre, uint32_t sub)
{
    (void)irq; (void)pre; (void)sub;
}

void HAL_NVIC_EnableIRQ(test_irq_t irq) { (void)irq; }

/* Hands the fake peripheral one received byte and runs the real ISR. */
void test_uart_feed(uint8_t b)
{
    extern void UART4_IRQHandler(void);

    fake_uart4_regs.RDR = b;
    fake_uart4_regs.ISR |= USART_ISR_RXNE_RXFNE;
    UART4_IRQHandler();
    fake_uart4_regs.ISR &= ~USART_ISR_RXNE_RXFNE;
}

void test_uart_raise(uint32_t isr_bits)
{
    fake_uart4_regs.ISR |= isr_bits;
}

uint32_t test_uart_isr(void)
{
    return fake_uart4_regs.ISR;
}

static uint32_t fake_tick_ms = 1000u;

/* Test-visible state. */
int      test_relay_energised[RELAY_COUNT];
int      test_relay_init_count;
/* Starts on LSE, as the board. */
RCC_TypeDef        test_rcc = { .BDCR = (1ul << RCC_BDCR_RTCSEL_Pos) };
RCC_TypeDef *const RCC      = &test_rcc;

int      test_din_init_count;
uint8_t  test_din_bits = 0x16u;

uint32_t HAL_GetTick(void)
{
    return fake_tick_ms;
}

void test_set_tick(uint32_t ms)
{
    fake_tick_ms = ms;
}

uint32_t test_uart4_baud = 115200u;

HAL_StatusTypeDef HAL_UART_Init(UART_HandleTypeDef *huart)
{
    test_uart4_baud = huart->Init.BaudRate;
    return HAL_OK;
}

/* ---- Core/Inc/relay.c ---------------------------------------------------- */

void Relay_Init(void)
{
    test_relay_init_count++;
    memset(test_relay_energised, 0, sizeof(test_relay_energised));
}

void Relay_On(RELAY_Name relay)
{
    if ((int)relay < RELAY_COUNT) { test_relay_energised[relay] = 1; }
}

void Relay_Off(RELAY_Name relay)
{
    if ((int)relay < RELAY_COUNT) { test_relay_energised[relay] = 0; }
}

/* ---- TestCase/common/port_din.c ------------------------------------------ */

/* Only so port_din.h's extern resolves; nothing on the host reads a pin. */
const port_din_t port_din_pins[PORT_DIN_COUNT] = { { NULL, 0u } };

void PortDin_Init(void)
{
    test_din_init_count++;
}

uint8_t PortDin_ReadBits(void)
{
    return test_din_bits;
}

/* ---- the indicator and the RTC: record what the firmware drove -------- */

int test_led_writes;
int test_led_high;
int test_led_configured;

/* One output latch per bank, so a pin reads back what was last written;
 * test_gpio_stuck_low models a pin that will not follow. */
uint16_t test_gpio_out[TEST_GPIO_BANKS];
uint16_t test_gpio_stuck_low[TEST_GPIO_BANKS];

static int gpio_bank(const GPIO_TypeDef *port)
{
    for (int i = 0; i < TEST_GPIO_BANKS; i++) {
        if (port == &test_gpio_banks[i]) { return i; }
    }
    return -1;
}

void HAL_GPIO_Init(GPIO_TypeDef *port, GPIO_InitTypeDef *init)
{
    if (port == GPIOE && init != NULL && init->Pin == GPIO_PIN_2) {
        test_led_configured++;
    }
}

void HAL_GPIO_WritePin(GPIO_TypeDef *port, uint16_t pin, GPIO_PinState state)
{
    int bank = gpio_bank(port);

    if (bank >= 0) {
        if (state == GPIO_PIN_SET) {
            test_gpio_out[bank] |= pin;
        } else {
            test_gpio_out[bank] &= (uint16_t)~pin;
        }
    }

    if (port != GPIOE || pin != GPIO_PIN_2) {
        return;
    }
    test_led_writes++;
    if (state == GPIO_PIN_SET) {
        test_led_high++;
    }
}

GPIO_PinState HAL_GPIO_ReadPin(GPIO_TypeDef *port, uint16_t pin)
{
    int bank = gpio_bank(port);
    uint16_t level;

    if (bank < 0) { return GPIO_PIN_RESET; }

    level = (uint16_t)(test_gpio_out[bank] & ~test_gpio_stuck_low[bank]);
    return (level & pin) ? GPIO_PIN_SET : GPIO_PIN_RESET;
}

/* A no-op: nothing here depends on time passing. */
void HAL_Delay(uint32_t ms)
{
    (void)ms;
}

RTC_HandleTypeDef hrtc;
int test_rtc_init_count;
int test_rtc_calendar_initialised = 1;
int test_rtc_runs;

void MX_RTC_Init(void)
{
    test_rtc_init_count++;
    hrtc.State = HAL_RTC_STATE_READY;
}

HAL_StatusTypeDef HAL_RTC_GetTime(RTC_HandleTypeDef *h, RTC_TimeTypeDef *t, uint32_t fmt)
{
    (void)h; (void)fmt;
    /* The simulated board counts on with the HAL tick, so the panel's board
     * clock moves (PORTTOOL-FLOW.md C.3.6); T4-01 keeps 13:45:07 fixed. */
    uint32_t s = 13u * 3600u + 45u * 60u + 7u +
                 (test_rtc_runs ? fake_tick_ms / 1000u : 0u);
    t->Hours = (uint8_t)((s / 3600u) % 24u);
    t->Minutes = (uint8_t)((s / 60u) % 60u);
    t->Seconds = (uint8_t)(s % 60u);
    return HAL_OK;
}

HAL_StatusTypeDef HAL_RTC_GetDate(RTC_HandleTypeDef *h, RTC_DateTypeDef *d, uint32_t fmt)
{
    (void)h; (void)fmt;
    d->Year = 26; d->Month = 9; d->Date = 7;
    return HAL_OK;
}
