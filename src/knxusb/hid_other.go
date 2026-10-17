//go:build !windows && !linux && !darwin

package knxusb

// Systems without a HID layer here (see ErrUnsupported).
func listHID() ([]DeviceInfo, error) { return nil, ErrUnsupported }

func openHID(string) (HID, error) { return nil, ErrUnsupported }
