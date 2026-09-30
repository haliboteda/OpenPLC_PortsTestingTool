/* Stand-in: only the one call the usb session makes. */
#ifndef PORTTOOL_HOSTTEST_STUB_USBD_CDC_IF_H_
#define PORTTOOL_HOSTTEST_STUB_USBD_CDC_IF_H_

#include "usbd_def.h"

uint8_t CDC_Transmit_FS(uint8_t *buf, uint16_t len);

#endif
