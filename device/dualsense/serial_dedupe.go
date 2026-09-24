package dualsense

import "github.com/Alia5/VIIPER/device"

// Serial numbers and MAC addresses in use by DualSense and DualSense Edge
// devices, shared so the two types never hand out the same identity.
var (
	serials = device.NewIdentityPool()
	macs    = device.NewIdentityPool()
)
