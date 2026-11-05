#include "uvc_darwin.h"

#include <stdlib.h>
#include <string.h>
#include <IOKit/IOKitLib.h>
#include <IOKit/IOCFPlugIn.h>
#include <IOKit/usb/IOUSBLib.h>
#include <CoreFoundation/CoreFoundation.h>

#define UVC_CLASS_VIDEO          14
#define UVC_SUBCLASS_CONTROL     1
#define UVC_CS_INTERFACE         0x24
#define UVC_VC_INPUT_TERMINAL    0x02
#define UVC_VC_PROCESSING_UNIT   0x05
#define UVC_ITT_CAMERA           0x0201

struct uvcx_handle {
    IOUSBDeviceInterface **dev;
    int iface;           // VideoControl interface number
    int camera_terminal;
    int processing_unit;
};

static IOUSBDeviceInterface **device_interface(io_service_t svc) {
    IOCFPlugInInterface **plug = NULL;
    SInt32 score;
    kern_return_t kr = IOCreatePlugInInterfaceForService(
        svc, kIOUSBDeviceUserClientTypeID, kIOCFPlugInInterfaceID, &plug, &score);
    if (kr != kIOReturnSuccess || !plug) return NULL;
    IOUSBDeviceInterface **dev = NULL;
    (*plug)->QueryInterface(plug, CFUUIDGetUUIDBytes(kIOUSBDeviceInterfaceID), (LPVOID *)&dev);
    (*plug)->Release(plug);
    return dev;
}

// Returns the VideoControl interface number, or -1 if this isn't a UVC device.
static int control_interface(IOUSBDeviceInterface **dev) {
    IOUSBFindInterfaceRequest req;
    req.bInterfaceClass = UVC_CLASS_VIDEO;
    req.bInterfaceSubClass = UVC_SUBCLASS_CONTROL;
    req.bInterfaceProtocol = kIOUSBFindInterfaceDontCare;
    req.bAlternateSetting = kIOUSBFindInterfaceDontCare;
    io_iterator_t it;
    if ((*dev)->CreateInterfaceIterator(dev, &req, &it) != kIOReturnSuccess) return -1;
    int num = -1;
    io_service_t svc = IOIteratorNext(it);
    if (svc) {
        num = 0;
        CFNumberRef ref = (CFNumberRef)IORegistryEntryCreateCFProperty(
            svc, CFSTR("bInterfaceNumber"), kCFAllocatorDefault, 0);
        if (ref) {
            CFNumberGetValue(ref, kCFNumberIntType, &num);
            CFRelease(ref);
        }
        IOObjectRelease(svc);
    }
    IOObjectRelease(it);
    return num;
}

static uint32_t location_of(io_service_t svc) {
    uint32_t loc = 0;
    CFNumberRef ref = (CFNumberRef)IORegistryEntryCreateCFProperty(
        svc, CFSTR("locationID"), kCFAllocatorDefault, 0);
    if (ref) {
        CFNumberGetValue(ref, kCFNumberSInt32Type, &loc);
        CFRelease(ref);
    }
    return loc;
}

static void name_of(io_service_t svc, char *buf, size_t len) {
    buf[0] = 0;
    CFStringRef ref = (CFStringRef)IORegistryEntryCreateCFProperty(
        svc, CFSTR("USB Product Name"), kCFAllocatorDefault, 0);
    if (ref) {
        CFStringGetCString(ref, buf, (CFIndex)len, kCFStringEncodingUTF8);
        CFRelease(ref);
    }
}

// Walks the configuration descriptor to find the real Camera Terminal and
// Processing Unit IDs. Many cameras use 1 and 2, but not all.
static void find_unit_ids(uvcx_handle *h) {
    h->camera_terminal = 1;
    h->processing_unit = 2;
    IOUSBConfigurationDescriptorPtr cfg = NULL;
    if ((*h->dev)->GetConfigurationDescriptorPtr(h->dev, 0, &cfg) != kIOReturnSuccess || !cfg) return;
    const uint8_t *p = (const uint8_t *)cfg;
    int total = USBToHostWord(cfg->wTotalLength);
    int in_vc = 0, found_ct = 0, found_pu = 0;
    for (int off = 0; off + 2 <= total;) {
        int len = p[off], type = p[off + 1];
        if (len < 2 || off + len > total) break;
        if (type == kUSBInterfaceDesc && len >= 9) {
            in_vc = p[off + 5] == UVC_CLASS_VIDEO && p[off + 6] == UVC_SUBCLASS_CONTROL;
        } else if (in_vc && type == UVC_CS_INTERFACE && len >= 4) {
            int subtype = p[off + 2];
            if (subtype == UVC_VC_INPUT_TERMINAL && len >= 6 && !found_ct) {
                int tt = p[off + 4] | (p[off + 5] << 8);
                if (tt == UVC_ITT_CAMERA) { h->camera_terminal = p[off + 3]; found_ct = 1; }
            } else if (subtype == UVC_VC_PROCESSING_UNIT && !found_pu) {
                h->processing_unit = p[off + 3];
                found_pu = 1;
            }
        }
        off += len;
    }
}

int uvcx_list(uvcx_info *out, int max) {
    CFMutableDictionaryRef match = IOServiceMatching(kIOUSBDeviceClassName);
    if (!match) return 0;
    io_iterator_t it;
    if (IOServiceGetMatchingServices(kIOMainPortDefault, match, &it) != kIOReturnSuccess) return 0;
    int n = 0;
    io_service_t svc;
    while ((svc = IOIteratorNext(it))) {
        IOUSBDeviceInterface **dev = device_interface(svc);
        if (dev) {
            if (control_interface(dev) >= 0 && n < max) {
                uvcx_info *info = &out[n++];
                memset(info, 0, sizeof(*info));
                (*dev)->GetDeviceVendor(dev, &info->vid);
                (*dev)->GetDeviceProduct(dev, &info->pid);
                info->location = location_of(svc);
                name_of(svc, info->name, sizeof(info->name));
            }
            (*dev)->Release(dev);
        }
        IOObjectRelease(svc);
    }
    IOObjectRelease(it);
    return n;
}

uvcx_handle *uvcx_open(uint32_t location) {
    CFMutableDictionaryRef match = IOServiceMatching(kIOUSBDeviceClassName);
    if (!match) return NULL;
    io_iterator_t it;
    if (IOServiceGetMatchingServices(kIOMainPortDefault, match, &it) != kIOReturnSuccess) return NULL;
    uvcx_handle *h = NULL;
    io_service_t svc;
    while (!h && (svc = IOIteratorNext(it))) {
        if (location_of(svc) == location) {
            IOUSBDeviceInterface **dev = device_interface(svc);
            if (dev) {
                int iface = control_interface(dev);
                if (iface >= 0) {
                    h = calloc(1, sizeof(*h));
                    h->dev = dev;
                    h->iface = iface;
                    find_unit_ids(h);
                } else {
                    (*dev)->Release(dev);
                }
            }
        }
        IOObjectRelease(svc);
    }
    IOObjectRelease(it);
    return h;
}

void uvcx_close(uvcx_handle *h) {
    if (!h) return;
    if (h->dev) (*h->dev)->Release(h->dev);
    free(h);
}

int uvcx_camera_terminal(uvcx_handle *h) { return h ? h->camera_terminal : 1; }
int uvcx_processing_unit(uvcx_handle *h) { return h ? h->processing_unit : 2; }

int uvcx_xfer(uvcx_handle *h, int request, int unit, int selector, int size, uint8_t *data) {
    if (!h || !h->dev) return UVCX_ERR_NO_DEVICE;
    IOUSBDevRequest req;
    int out = request == 0x01; // SET_CUR
    req.bmRequestType = USBmakebmRequestType(out ? kUSBOut : kUSBIn, kUSBClass, kUSBInterface);
    req.bRequest = (UInt8)request;
    req.wValue = (UInt16)(selector << 8);
    req.wIndex = (UInt16)((unit << 8) | h->iface);
    req.wLength = (UInt16)size;
    req.wLenDone = 0;
    req.pData = data;

    // Opening is best-effort: control requests usually go through even when
    // another process (e.g. AVFoundation) holds the device.
    int opened = 0;
    kern_return_t kr = (*h->dev)->USBDeviceOpen(h->dev);
    if (kr == kIOReturnSuccess) {
        opened = 1;
    } else if (kr == kIOReturnExclusiveAccess) {
        if ((*h->dev)->USBDeviceOpenSeize(h->dev) == kIOReturnSuccess) opened = 1;
    }
    kr = (*h->dev)->DeviceRequest(h->dev, &req);
    if (opened) (*h->dev)->USBDeviceClose(h->dev);
    return kr == kIOReturnSuccess ? UVCX_OK : UVCX_ERR_REQUEST;
}
