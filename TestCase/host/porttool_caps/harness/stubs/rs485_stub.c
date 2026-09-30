/*
 * Fake RS485 transceiver.
 *
 * The queue is the point. RS485 is the first loop=link port: the board sends a
 * number on the A/B pair and has to take the reply back off that same pair.
 * So the test needs to be able to say "the far end answered", "the far end said
 * nothing", and "something arrived that was not a number" - the three states
 * the frame has to tell apart. test_rs485_reply() queues a line as if it came
 * off the pair.
 *
 * *** Half duplex is not simulated, and it does not need to be: the real
 * *** constraint is one net driving /RE and DE together, so the board cannot
 * *** hear itself. Nothing here echoes what was sent, which is exactly that
 * *** behaviour - a reply has to be fed in deliberately.
 *
 * *** The real port_rs485.c is NOT compiled here. Its direction handling and
 * *** the USART2 setup need the HAL UART module, and the timing that actually
 * *** matters - whether the driver is off before the last bit leaves - is
 * *** measured on the pair, not decided on a PC.
 */

#include "port_rs485.h"

#include <string.h>

/* Test-visible state. */
int      test_rs485_init_count;
uint32_t test_rs485_baud;
int      test_rs485_driving;          /* 1 while the driver is enabled */
char     test_rs485_sent[256];        /* everything the board put on the pair */
uint32_t test_rs485_sent_len;

static uint8_t  rx_queue[256];
static uint32_t rx_head, rx_tail;

/* Queues one line as if the far end had sent it, terminator included. The
 * terminator is added here so a transcript never has to spell an escape. */
void test_rs485_reply(const char *line)
{
    while (*line != '\0' && rx_tail < sizeof(rx_queue)) {
        rx_queue[rx_tail++] = (uint8_t)*line++;
    }
    if (rx_tail < sizeof(rx_queue)) {
        rx_queue[rx_tail++] = (uint8_t)'\n';
    }
}

void test_rs485_reset(void)
{
    rx_head = rx_tail = 0;
    test_rs485_sent_len = 0;
    test_rs485_sent[0] = '\0';
}

int PortRs485_Init(uint32_t baud)
{
    test_rs485_init_count++;
    test_rs485_baud = baud;
    return 1;
}

void PortRs485_DriveEnable(int on)
{
    test_rs485_driving = on;
}

int PortRs485_SendRaw(const uint8_t *data, uint16_t len)
{
    for (uint16_t i = 0; i < len; i++) {
        if (test_rs485_sent_len + 1u < sizeof(test_rs485_sent)) {
            test_rs485_sent[test_rs485_sent_len++] = (char)data[i];
            test_rs485_sent[test_rs485_sent_len] = '\0';
        }
    }
    return 0;   /* HAL_OK */
}

int PortRs485_Send(const uint8_t *data, uint16_t len)
{
    PortRs485_DriveEnable(1);
    int rc = PortRs485_SendRaw(data, len);
    PortRs485_DriveEnable(0);
    return rc;
}

/* Overruns the real driver would have counted. Settable so a test can assert
 * that a lost byte shows up in the frame instead of being hidden - on a board
 * an unreported overrun is what made the pair look dead. */
uint32_t test_rs485_overruns;

uint32_t PortRs485_Overruns(void)
{
    return test_rs485_overruns;
}

void PortRs485_ResetOverruns(void)
{
    test_rs485_overruns = 0;
}

int PortRs485_RecvByte(uint8_t *out)
{
    if (rx_head >= rx_tail) {
        return 0;
    }
    *out = rx_queue[rx_head++];
    return 1;
}
