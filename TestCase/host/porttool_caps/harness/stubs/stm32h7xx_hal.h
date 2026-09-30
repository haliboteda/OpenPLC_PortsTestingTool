/*
 * Stand-in for ST's stm32h7xx_hal.h, and the only header this harness fakes.
 *
 * Everything above it is the firmware's own: the real Core/Inc/main.h, the real
 * Core/Inc/usart.h, the real Core/Inc/relay.h and the real
 * TestCase/common/port_din.h all compile unchanged against this. That is what
 * makes the channel counts and pin macros the test sees the firmware's own
 * rather than a copy that could drift.
 *
 * Only the surface the port tool touches is declared. Anything it does not use
 * is deliberately absent, so a future porttool_*.c that reaches for more HAL
 * fails loudly here instead of quietly not being covered.
 */

#ifndef PORTTOOL_HOSTTEST_STUB_HAL_H_
#define PORTTOOL_HOSTTEST_STUB_HAL_H_

#include <stdint.h>
#include <stddef.h>

typedef enum {
    HAL_OK = 0,
    HAL_ERROR,
    HAL_BUSY,
    HAL_TIMEOUT,
} HAL_StatusTypeDef;

typedef struct { uint32_t opaque; } GPIO_TypeDef;

/* Enough of a USART for poll_command()'s receive-error handling to compile and
 * be testable. Only the two registers it touches, with the real bit positions
 * (checked against Drivers/CMSIS/.../stm32h743xx.h): a stub with invented
 * numbers would let the test agree with itself and not with the board. */
typedef struct {
    volatile uint32_t ISR;
    volatile uint32_t ICR;
    volatile uint32_t RDR;
} USART_TypeDef;

#define USART_ISR_RXNE_RXFNE (1UL << 5)
#define USART_ISR_FE     (1UL << 1)
#define USART_ISR_NE     (1UL << 2)
#define USART_ISR_ORE    (1UL << 3)
#define USART_ICR_FECF   (1UL << 1)
#define USART_ICR_NECF   (1UL << 2)
#define USART_ICR_ORECF  (1UL << 3)

typedef enum {
    HAL_UART_STATE_RESET   = 0x00,
    HAL_UART_STATE_READY   = 0x20,
    HAL_UART_STATE_BUSY_RX = 0x22,
} HAL_UART_StateTypeDef;

#define HAL_UART_ERROR_NONE 0x00000000U

typedef struct {
    USART_TypeDef        *Instance;
    uint32_t              ErrorCode;
    HAL_UART_StateTypeDef RxState;
} UART_HandleTypeDef;

/* The tool's receive ISR reaches the peripheral through UART4 rather than
 * through the handle, so the harness has to provide it as the same pointer
 * huart4.Instance carries - see hal_stub.c. */
extern USART_TypeDef *const UART4;

#define UART_IT_RXNE  0x0525U
typedef enum { UART4_IRQn = 52 } test_irq_t;

void HAL_NVIC_SetPriority(test_irq_t irq, uint32_t pre, uint32_t sub);
void HAL_NVIC_EnableIRQ(test_irq_t irq);

/* Enabling the receive interrupt has no meaning with no hardware: the harness
 * calls UART4_IRQHandler() by hand. Defined so the real code compiles
 * unchanged rather than being #ifdef'd for the test. */
#define __HAL_UART_ENABLE_IT(h, it) ((void)(h), (void)(it))

typedef enum { GPIO_PIN_RESET = 0, GPIO_PIN_SET } GPIO_PinState;

/* Real bit values: main.h builds pin masks out of these and relay.c compares
 * them, so wrong numbers here would make the test agree with itself and not
 * with the board. */
#define GPIO_PIN_0   ((uint16_t)0x0001)
#define GPIO_PIN_1   ((uint16_t)0x0002)
#define GPIO_PIN_2   ((uint16_t)0x0004)
#define GPIO_PIN_3   ((uint16_t)0x0008)
#define GPIO_PIN_4   ((uint16_t)0x0010)
#define GPIO_PIN_5   ((uint16_t)0x0020)
#define GPIO_PIN_6   ((uint16_t)0x0040)
#define GPIO_PIN_7   ((uint16_t)0x0080)
#define GPIO_PIN_8   ((uint16_t)0x0100)
#define GPIO_PIN_9   ((uint16_t)0x0200)
#define GPIO_PIN_10  ((uint16_t)0x0400)
#define GPIO_PIN_11  ((uint16_t)0x0800)
#define GPIO_PIN_12  ((uint16_t)0x1000)
#define GPIO_PIN_13  ((uint16_t)0x2000)
#define GPIO_PIN_14  ((uint16_t)0x4000)
#define GPIO_PIN_15  ((uint16_t)0x8000)

/* Distinct addresses so a mixed-up port is a distinguishable pointer, not a
 * silent alias of another one. */
#define TEST_GPIO_BANKS 11
extern GPIO_TypeDef test_gpio_banks[TEST_GPIO_BANKS];
#define GPIOA (&test_gpio_banks[0])
#define GPIOB (&test_gpio_banks[1])
#define GPIOC (&test_gpio_banks[2])
#define GPIOD (&test_gpio_banks[3])
#define GPIOE (&test_gpio_banks[4])
#define GPIOF (&test_gpio_banks[5])
#define GPIOG (&test_gpio_banks[6])
#define GPIOH (&test_gpio_banks[7])
#define GPIOI (&test_gpio_banks[8])
#define GPIOJ (&test_gpio_banks[9])
#define GPIOK (&test_gpio_banks[10])

uint32_t HAL_GetTick(void);
HAL_StatusTypeDef HAL_UART_Receive(UART_HandleTypeDef *huart, uint8_t *data,
                                   uint16_t size, uint32_t timeout);

/* The real UID_BASE is a flash address the MCU casts to a pointer. Here it is
 * the address of a fixed array, so pt.id has something deterministic to read.
 * uintptr_t rather than uint32_t: the host is 64-bit and porttool.c casts this
 * straight to a pointer, which a 32-bit value would truncate. */
extern uint32_t test_uid_words[3];
#define UID_BASE ((uintptr_t)test_uid_words)

/* ---- GPIO and the millisecond delay, for pt.run led.blink -------------
 * The indicator has no readback on the board either, so the stub only has to
 * let the code run and count what it drove. */
typedef struct {
    uint32_t Pin;
    uint32_t Mode;
    uint32_t Pull;
    uint32_t Speed;
    uint32_t Alternate;
} GPIO_InitTypeDef;

#define GPIO_MODE_OUTPUT_PP    0x01u
#define GPIO_NOPULL            0x00u
#define GPIO_SPEED_FREQ_LOW    0x00u
#define __HAL_RCC_GPIOE_CLK_ENABLE()  ((void)0)
#define __HAL_RCC_GPIOD_CLK_ENABLE()  ((void)0)

void HAL_GPIO_Init(GPIO_TypeDef *port, GPIO_InitTypeDef *init);
void HAL_GPIO_WritePin(GPIO_TypeDef *port, uint16_t pin, GPIO_PinState state);
GPIO_PinState HAL_GPIO_ReadPin(GPIO_TypeDef *port, uint16_t pin);
void HAL_Delay(uint32_t ms);

extern int test_led_writes;   /* every WritePin on PE2, high and low alike */
extern int test_led_high;     /* how many of those drove it high */
extern int test_led_configured;

/* What each bank's pins were last driven to, and which of them refuse to
 * follow. Setting a bit in test_gpio_stuck_low models a dead pin, which is the
 * fault pt.run rs485.pins exists to separate from an unplugged pair. */
extern uint16_t test_gpio_out[TEST_GPIO_BANKS];
extern uint16_t test_gpio_stuck_low[TEST_GPIO_BANKS];

/* ---- RCC's backup-domain register -------------------------------------
 *
 * Only BDCR, and only because rtc.read reports which oscillator the calendar
 * is counting by reading RTCSEL out of it. It used to report the literal
 * "lsi", which went stale the day the project moved to LSE - so the field
 * reads the register now, and a test can move the register to check that it
 * really follows.
 */
typedef struct { uint32_t BDCR; } RCC_TypeDef;

#define RCC_BDCR_RTCSEL_Pos  8u
#define RCC_BDCR_RTCSEL      (3ul << RCC_BDCR_RTCSEL_Pos)

extern RCC_TypeDef  test_rcc;      /* a test writes BDCR through this */
extern RCC_TypeDef *const RCC;     /* the firmware reads it through this */

/* ---- RTC, for pt.run rtc.read ----------------------------------------- */
typedef enum { HAL_RTC_STATE_RESET = 0, HAL_RTC_STATE_READY } HAL_RTCStateTypeDef;

typedef struct { uint32_t opaque; } RTC_TypeDef;

typedef struct {
    uint32_t HourFormat;
    uint32_t AsynchPrediv;
    uint32_t SynchPrediv;
    uint32_t OutPut;
    uint32_t OutPutPolarity;
    uint32_t OutPutType;
    uint32_t OutPutRemap;
} RTC_InitTypeDef;

typedef struct {
    RTC_TypeDef        *Instance;
    RTC_InitTypeDef     Init;
    HAL_RTCStateTypeDef State;
} RTC_HandleTypeDef;

typedef struct {
    uint8_t Hours, Minutes, Seconds;
    uint32_t SubSeconds, SecondFraction, TimeFormat, DayLightSaving, StoreOperation;
} RTC_TimeTypeDef;

typedef struct { uint8_t WeekDay, Month, Date, Year; } RTC_DateTypeDef;

#define RTC_FORMAT_BIN   0x00u
#define RTC_HOURFORMAT_24 0x00u
#define RTC_OUTPUT_DISABLE 0x00u
#define RTC_OUTPUT_POLARITY_HIGH 0x00u
#define RTC_OUTPUT_TYPE_OPENDRAIN 0x00u
#define RTC_OUTPUT_REMAP_NONE 0x00u

/* The real macro reads the INITS flag out of the peripheral. Here the harness
 * sets it, so a board whose calendar was never set is testable. */
extern int test_rtc_calendar_initialised;
#define __HAL_RTC_IS_CALENDAR_INITIALIZED(h)  ((uint32_t)test_rtc_calendar_initialised)

extern RTC_HandleTypeDef hrtc;
extern int test_rtc_init_count;

void MX_RTC_Init(void);
HAL_StatusTypeDef HAL_RTC_GetTime(RTC_HandleTypeDef *h, RTC_TimeTypeDef *t, uint32_t fmt);
HAL_StatusTypeDef HAL_RTC_GetDate(RTC_HandleTypeDef *h, RTC_DateTypeDef *d, uint32_t fmt);

/* ---- FDCAN, enough for the CAN session's mode handling ----------------
 * The driver itself (port_can.c) is stubbed, so only the names the session
 * mentions have to exist. */
typedef struct { uint32_t opaque; } FDCAN_HandleTypeDef;

#define FDCAN_MODE_NORMAL             0x00000000u
#define FDCAN_MODE_RESTRICTED_OPERATION 0x00000001u
#define FDCAN_MODE_BUS_MONITORING     0x00000002u
#define FDCAN_MODE_INTERNAL_LOOPBACK  0x00000003u
#define FDCAN_MODE_EXTERNAL_LOOPBACK  0x00000004u

#endif /* PORTTOOL_HOSTTEST_STUB_HAL_H_ */
