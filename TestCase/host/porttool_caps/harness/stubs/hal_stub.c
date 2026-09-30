/*
 * Fake hardware for the port tool host test: a frozen clock, a serial port
 * that never has a byte waiting, and the two pieces of board hardware the two
 * existing sessions drive.
 *
 * The relay stub remembers what it was told to do so the test can check that
 * pt.stop really releases every relay - the one side effect the port tool is
 * responsible for putting back.
 */

#include "main.h"
#include "relay.h"
#include "port_din.h"

#include <string.h>

uint32_t test_uid_words[3] = { 0x00340041u, 0x31355114u, 0x39303538u };

GPIO_TypeDef test_gpio_banks[11];

/* huart4 needs a register block to point at, and writing ICR has to actually
 * clear ISR - otherwise the test could not tell a firmware that clears the
 * flag from one that reads it and moves on, which is exactly the bug this
 * models (a latched receive error made the board deaf to commands). */
static USART_TypeDef fake_uart4_regs;
UART_HandleTypeDef huart4 = { &fake_uart4_regs, 0, HAL_UART_STATE_READY };
USART_TypeDef *const UART4 = &fake_uart4_regs;

void HAL_NVIC_SetPriority(test_irq_t irq, uint32_t pre, uint32_t sub)
{
    (void)irq; (void)pre; (void)sub;
}

void HAL_NVIC_EnableIRQ(test_irq_t irq) { (void)irq; }

/* Hands the fake peripheral one received byte and runs the real ISR, so the
 * test exercises the tool's own receive path rather than a copy of it. */
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
/* Starts on LSE, the way the board has been since 2026-09-10. A test moves it
 * to prove rtc.read follows the register rather than describing it. */
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

HAL_StatusTypeDef HAL_UART_Receive(UART_HandleTypeDef *huart, uint8_t *data,
                                   uint16_t size, uint32_t timeout)
{
    (void)huart; (void)data; (void)size; (void)timeout;
    return HAL_TIMEOUT;   /* nothing ever arrives; the test calls dispatch() */
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

void Relay_Toggle(RELAY_Name relay)
{
    if ((int)relay < RELAY_COUNT) {
        test_relay_energised[relay] = !test_relay_energised[relay];
    }
}

/* ---- TestCase/common/port_din.c ------------------------------------------ */

/* Only element 0 is spelled out; the rest are implicitly zero. Nothing on the
 * host dereferences a pin, the table exists so port_din.h's extern resolves. */
const port_din_t port_din_pins[PORT_DIN_COUNT] = { { NULL, 0u, NULL } };

void PortDin_Init(void)
{
    test_din_init_count++;
}

uint8_t PortDin_ReadBits(void)
{
    return test_din_bits;
}

/* ---- the indicator and the RTC, for the two pt.run targets ------------
 *
 * Neither has a readback on the real board, so neither stub pretends to
 * observe anything: they record what the firmware drove and asked for, which
 * is exactly what the OK line is allowed to claim. */

int test_led_writes;
int test_led_high;
int test_led_configured;

/* One output latch per bank, so a pin reads back what was last written to it -
 * which is what a healthy push-pull output does, and the whole of what
 * pt.run rs485.pins checks.
 *
 * test_gpio_stuck_low is how a test models the fault that target exists to
 * find: a pin that will not follow. Without it the check could only ever be
 * seen passing, and a check that has never been seen failing is not evidence
 * of anything. */
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

/* A no-op on purpose: led.blink asks for three seconds of real time, and a
 * contract test that took three seconds to check a reply format would get
 * skipped. Nothing here depends on time actually passing. */
void HAL_Delay(uint32_t ms)
{
    (void)ms;
}

RTC_HandleTypeDef hrtc;
int test_rtc_init_count;
int test_rtc_calendar_initialised = 1;

void MX_RTC_Init(void)
{
    test_rtc_init_count++;
    hrtc.State = HAL_RTC_STATE_READY;
}

HAL_StatusTypeDef HAL_RTC_GetTime(RTC_HandleTypeDef *h, RTC_TimeTypeDef *t, uint32_t fmt)
{
    (void)h; (void)fmt;
    t->Hours = 13; t->Minutes = 45; t->Seconds = 7;
    return HAL_OK;
}

HAL_StatusTypeDef HAL_RTC_GetDate(RTC_HandleTypeDef *h, RTC_DateTypeDef *d, uint32_t fmt)
{
    (void)h; (void)fmt;
    d->Year = 26; d->Month = 9; d->Date = 7;
    return HAL_OK;
}
