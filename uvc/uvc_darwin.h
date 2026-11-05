// IOKit layer for sending raw UVC control requests on macOS.
//
// Talks to the camera with USB control transfers on the VideoControl
// interface, so it works alongside AVFoundation (which owns the video
// stream) without detaching any driver.
#ifndef UVCCTL_UVC_DARWIN_H
#define UVCCTL_UVC_DARWIN_H

#include <stdint.h>

#define UVCX_OK            0
#define UVCX_ERR_NO_DEVICE -1
#define UVCX_ERR_REQUEST   -2

typedef struct {
    uint16_t vid;
    uint16_t pid;
    uint32_t location;   // IOKit locationID; stable while plugged into the same port
    char     name[128];  // USB product string
} uvcx_info;

typedef struct uvcx_handle uvcx_handle;

// Fills up to max entries with every USB device that has a UVC
// VideoControl interface. Returns the number found.
int uvcx_list(uvcx_info *out, int max);

// Opens the UVC device at the given IOKit locationID. NULL if not found.
uvcx_handle *uvcx_open(uint32_t location);
void uvcx_close(uvcx_handle *h);

// Camera Terminal and Processing Unit IDs, read from the device's
// class-specific descriptors (falls back to 1 and 2).
int uvcx_camera_terminal(uvcx_handle *h);
int uvcx_processing_unit(uvcx_handle *h);

// One UVC class request. request is SET_CUR (0x01) or a GET_* (0x81..0x87).
// data holds size bytes, little-endian. Returns UVCX_OK or an error.
int uvcx_xfer(uvcx_handle *h, int request, int unit, int selector, int size, uint8_t *data);

#endif
