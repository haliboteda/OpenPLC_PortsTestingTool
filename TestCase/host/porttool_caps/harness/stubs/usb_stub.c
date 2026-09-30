/*
 * Fake USB CDC device.
 *
 * What the session needs from a USB stack is small: a device state that says
 * how far enumeration got, a transmit that can refuse, and a way for the test
 * to push bytes in as if the PC had sent them. Everything else about USB is
 * absent on purpose.
 *
 * *** The interesting state is "enumerated but nobody opened the port" versus
 * *** "configured". A board with the cable in but no application at the other
 * *** end looks identical to a dead link in every counter, and telling those
 * *** apart is why the frame carries state= at all - so the stub has to be able
 * *** to sit in either.
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
    /* A host that enumerated it. A test wanting the other case sets the state
     * back down afterwards. */
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
    /* Kept so a test can read back what the board put on the wire. The tail is
     * dropped rather than wrapped: source mode writes far more than any
     * assertion needs, and wrapping would make the echo lines unfindable. */
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

void test_usb_reset(void)
{
    memset(&hUsbDeviceFS, 0, sizeof(hUsbDeviceFS));
    test_usb_inits = 0;
    test_usb_busy = 0;
    test_usb_sent[0] = '\0';
    test_usb_sent_len = 0;
}
