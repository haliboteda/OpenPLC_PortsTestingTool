/*
 * Stand-in for ST's stm32h7xx_hal.h. The firmware's own main.h, usart.h,
 * relay.h and port_*.h compile unchanged against it, so channel counts and pin
 * macros are the firmware's. Only the surface the port tool touches is
 * declared, so reaching for more HAL fails here loudly.
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

/* The USART registers UART4_IRQHandler touches, with the real bit positions
 * (Drivers/CMSIS/.../stm32h743xx.h). */
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

typedef struct {
    uint32_t BaudRate;
} UART_InitTypeDef;

typedef struct {
    USART_TypeDef        *Instance;
    uint32_t              ErrorCode;
    HAL_UART_StateTypeDef RxState;
    UART_InitTypeDef      Init;
} UART_HandleTypeDef;

/* The receive ISR uses UART4, the same pointer huart4.Instance carries. */
extern USART_TypeDef *const UART4;

#define UART_IT_RXNE  0x0525U
typedef enum { UART4_IRQn = 52 } test_irq_t;

void HAL_NVIC_SetPriority(test_irq_t irq, uint32_t pre, uint32_t sub);
void HAL_NVIC_EnableIRQ(test_irq_t irq);

/* No-op: the harness calls UART4_IRQHandler() by hand. */
#define __HAL_UART_ENABLE_IT(h, it) ((void)(h), (void)(it))

typedef enum { GPIO_PIN_RESET = 0, GPIO_PIN_SET } GPIO_PinState;

/* Real bit values: main.h builds pin masks out of these. */
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

/* Distinct addresses, so a mixed-up port is a different pointer. */
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
/* Records the rate the control line was switched to (test_uart4_baud). */
HAL_StatusTypeDef HAL_UART_Init(UART_HandleTypeDef *huart);

/* UID_BASE points at a fixed array; uintptr_t, since the host is 64-bit. */
extern uint32_t test_uid_words[3];
#define UID_BASE ((uintptr_t)test_uid_words)

/* Same for sector 15, whose first 8 KiB is the calibration area pt.cal reads
 * (porttool_calw.h falls back to CALIB_AREA_ADDR without this).
 * test_calib_write() puts a valid record in it with the real CRC,
 * test_calib_erase() makes the whole sector erased flash. */
extern uint8_t test_calib_area[];
#define PORTTOOL_CALIB_BASE ((uintptr_t)test_calib_area)
void test_calib_erase(void);
void test_calib_write(const uint32_t uid[3], const float gain_offset[8]);

/* ---- flash, for pt.calw -----------------------------------------------
 * The real constants, so porttool_calw.c's checks on them hold here too. The
 * stub flash is test_calib_area: erase fills it with 0xFF, a program call
 * writes one 32-byte word and refuses a word already written (ECC on H7). */
#define FLASH_BANK2_BASE              0x08100000UL
#define FLASH_SECTOR_SIZE             0x00020000UL
#define FLASH_SECTOR_7                7U
#define FLASH_BANK_2                  0x02U
#define FLASH_TYPEERASE_SECTORS       0x00U
#define FLASH_VOLTAGE_RANGE_3         0x20U
#define FLASH_TYPEPROGRAM_FLASHWORD   0x01U
#define FLASH_FLAG_ALL_ERRORS_BANK2   0x87EE0000U
#define FLASH_FLAG_SNECCERR_BANK2     0x80200000U
#define FLASH_FLAG_DBECCERR_BANK2     0x80400000U
#define FLASH_OPTCR_SWAP_BANK         0x80000000U

typedef struct { uint32_t OPTCR; } FLASH_TypeDef;
extern FLASH_TypeDef test_flash_regs;
#define FLASH (&test_flash_regs)

typedef struct {
    uint32_t TypeErase;
    uint32_t Banks;
    uint32_t Sector;
    uint32_t NbSectors;
    uint32_t VoltageRange;
} FLASH_EraseInitTypeDef;

HAL_StatusTypeDef HAL_FLASH_Unlock(void);
HAL_StatusTypeDef HAL_FLASH_Lock(void);
HAL_StatusTypeDef HAL_FLASHEx_Erase(FLASH_EraseInitTypeDef *e, uint32_t *sector_error);
uint32_t HAL_FLASH_GetError(void);
/* The real one takes uint32_t addresses; a 64-bit host pointer would not fit. */
HAL_StatusTypeDef test_flash_program(uint32_t type, uintptr_t dst, uintptr_t src);
#define HAL_FLASH_Program(t, dst, src) test_flash_program((t), (uintptr_t)(dst), (uintptr_t)(src))
#define __HAL_FLASH_CLEAR_FLAG_BANK2(f) ((void)(f))
#define __HAL_FLASH_GET_FLAG_BANK2(f)   (0)

/* What the flash was asked to do. stray counts program calls outside sector
 * 15 that reached the HAL - the guard in porttool_calw.c must keep it 0.
 * fail_erase makes the next erase fail. */
extern unsigned test_flash_erases, test_flash_programs, test_flash_stray;
extern int test_flash_fail_erase;

/* ---- GPIO and the millisecond delay ----------------------------------- */
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

/* What each bank's pins were last driven to; a bit in test_gpio_stuck_low
 * models a dead pin (for pt.run rs485.pins). */
extern uint16_t test_gpio_out[TEST_GPIO_BANKS];
extern uint16_t test_gpio_stuck_low[TEST_GPIO_BANKS];

/* ---- RCC BDCR, whose RTCSEL rtc.read reports; a test can move it ------- */
typedef struct { uint32_t BDCR; } RCC_TypeDef;

#define RCC_BDCR_RTCSEL_Pos  8u
#define RCC_BDCR_RTCSEL      (3ul << RCC_BDCR_RTCSEL_Pos)

extern RCC_TypeDef  test_rcc;      /* a test writes BDCR through this */
extern RCC_TypeDef *const RCC;     /* the firmware reads it through this */

/* ---- RTC, for pt.run rtc.read ----------------------------------------- */
typedef enum { HAL_RTC_STATE_RESET = 0, HAL_RTC_STATE_READY } HAL_RTCStateTypeDef;

typedef struct { uint32_t opaque; } RTC_TypeDef;

typedef struct {
    RTC_TypeDef        *Instance;
    HAL_RTCStateTypeDef State;
} RTC_HandleTypeDef;

typedef struct {
    uint8_t Hours, Minutes, Seconds;
    uint32_t SubSeconds, SecondFraction, TimeFormat, DayLightSaving, StoreOperation;
} RTC_TimeTypeDef;

typedef struct { uint8_t WeekDay, Month, Date, Year; } RTC_DateTypeDef;

#define RTC_FORMAT_BIN   0x00u

/* The real macro reads INITS; here the harness sets it. */
extern int test_rtc_calendar_initialised;
#define __HAL_RTC_IS_CALENDAR_INITIALIZED(h)  ((uint32_t)test_rtc_calendar_initialised)

extern RTC_HandleTypeDef hrtc;
extern int test_rtc_init_count;

void MX_RTC_Init(void);
HAL_StatusTypeDef HAL_RTC_GetTime(RTC_HandleTypeDef *h, RTC_TimeTypeDef *t, uint32_t fmt);
HAL_StatusTypeDef HAL_RTC_GetDate(RTC_HandleTypeDef *h, RTC_DateTypeDef *d, uint32_t fmt);

/* ---- FDCAN: only the mode names the CAN session uses (port_can.c is
 * stubbed) */
#define FDCAN_MODE_NORMAL             0x00000000u
#define FDCAN_MODE_BUS_MONITORING     0x00000002u
#define FDCAN_MODE_INTERNAL_LOOPBACK  0x00000003u
#define FDCAN_MODE_EXTERNAL_LOOPBACK  0x00000004u

#endif /* PORTTOOL_HOSTTEST_STUB_HAL_H_ */
