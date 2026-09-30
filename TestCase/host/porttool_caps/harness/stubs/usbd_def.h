/*
 * Stand-in for ST's usbd_def.h - only the device states and status codes the
 * usb session reads.
 *
 * The real header drags in the whole class framework; nothing here needs it,
 * and a session reaching for more of it should fail loudly at this line rather
 * than quietly not be covered.
 */

#ifndef PORTTOOL_HOSTTEST_STUB_USBD_DEF_H_
#define PORTTOOL_HOSTTEST_STUB_USBD_DEF_H_

#include <stdint.h>

#define USBD_STATE_DEFAULT    0x01U
#define USBD_STATE_ADDRESSED  0x02U
#define USBD_STATE_CONFIGURED 0x03U
#define USBD_STATE_SUSPENDED  0x04U

typedef enum {
    USBD_OK = 0U,
    USBD_BUSY,
    USBD_EMEM,
    USBD_FAIL,
} USBD_StatusTypeDef;

typedef struct {
    uint32_t dev_state;
} USBD_HandleTypeDef;

#endif /* PORTTOOL_HOSTTEST_STUB_USBD_DEF_H_ */
