/* Stand-in: only the entry point the usb session calls. */
#ifndef PORTTOOL_HOSTTEST_STUB_USB_DEVICE_H_
#define PORTTOOL_HOSTTEST_STUB_USB_DEVICE_H_

#include "usbd_def.h"

void MX_USB_DEVICE_Init(void);

/* Test-visible state, the way the other stubs do it. */
extern int      test_usb_inits;
extern int      test_usb_busy;
extern char     test_usb_sent[512];
extern uint32_t test_usb_sent_len;

void test_usb_set_state(uint8_t state);
void test_usb_feed(const char *s);
void test_usb_reset(void);

#endif
