/*
 * Fake USB CDC device: a device state, a transmit that can refuse, and a way
 * for the test to push bytes in as if the PC had sent them.
 */

#include "usbd_def.h"
#include "usbd_cdc_if.h"
#include "usb_device.h"
#include "porttool.h"   /* PortUsb_Received - the real declaration */

#include <string.h>

USBD_HandleTypeDef hUsbDeviceFS;

int      test_usb_inits;
int      test_usb_busy;          /* 1 = the endpoint refuses every write */
char     test_usb_sent[512];
uint32_t test_usb_sent_len;

void MX_USB_DEVICE_Init(void)
{
    test_usb_inits++;
    /* A host that enumerated it; a test can set the state back down. */
    hUsbDeviceFS.dev_state = USBD_STATE_CONFIGURED;
}

uint8_t CDC_Transmit_FS(uint8_t *buf, uint16_t len)
{
    if (test_usb_busy) {
        return USBD_BUSY;
    }
    if (hUsbDeviceFS.dev_state != USBD_STATE_CONFIGURED) {
        return USBD_FAIL;
    }
    /* Kept for the test; the tail is dropped, not wrapped. */
    if (test_usb_sent_len < sizeof(test_usb_sent) - 1u) {
        uint32_t room = (uint32_t)(sizeof(test_usb_sent) - 1u - test_usb_sent_len);
        uint32_t n = (len < room) ? len : room;
        memcpy(test_usb_sent + test_usb_sent_len, buf, n);
        test_usb_sent_len += n;
        test_usb_sent[test_usb_sent_len] = '\0';
    }
    return USBD_OK;
}

/* ---- what the test drives ---------------------------------------------- */

void test_usb_set_state(uint8_t state)
{
    hUsbDeviceFS.dev_state = state;
}

void test_usb_feed(const char *s)
{
    if (s == NULL) {
        return;
    }
    PortUsb_Received((const uint8_t *)s, (uint32_t)strlen(s));
}

