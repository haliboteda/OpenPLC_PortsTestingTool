/*
 * Fake RS485 transceiver. test_rs485_reply() queues a line as if the far end
 * sent it. Nothing echoes what was sent, as on the board (it cannot hear
 * itself). The real port_rs485.c needs the HAL UART and is not compiled here.
 */

#include "port_rs485.h"

#include <string.h>

/* Test-visible state. */
uint32_t test_rs485_baud;
int      test_rs485_driving;          /* 1 while the driver is enabled */
char     test_rs485_sent[256];        /* everything the board put on the pair */
uint32_t test_rs485_sent_len;

static uint8_t  rx_queue[256];
static uint32_t rx_head, rx_tail;

/* Queues one line as if the far end had sent it; adds the terminator. */
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

/* Overruns the real driver would have counted. */
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
