package main

/*
#include <stdint.h>
*/
import "C"

import (
	"fmt"
	"sync/atomic"
	"unsafe"

	"github.com/Alia5/VIIPER/device/dualshock4"
	"github.com/Alia5/VIIPER/device/xbox360"
)

// Typed fast path for high-frequency xbox360 input updates.
//
// The generic viiper_device_set_input takes the global mutex, does a map
// lookup, copies the buffer via C.GoBytes (one heap allocation per call) and
// type-switches on every report. For callers submitting at 125-1000 Hz that
// overhead dominates. The fast path resolves the device once into a handle;
// per-report cost is then a single atomic load plus the state update.
//
// A handle is a slot index plus that slot's generation. Slots live in a
// copy-on-write slice written under the global mu on the (cold) open and
// remove paths and read lock-free on the (hot) submit path.
// viiper_device_remove releases every slot that refers to the removed device,
// so the table no longer pins it and a stale handle is refused (-1) instead of
// silently accepting reports for a device that is gone. A freed slot is reused
// under a new generation, so an old handle never reaches a new device.
// viiper_shutdown clears the table; handles must be re-opened after re-init.

const (
	handleIndexBits = 12
	handleIndexMask = 1<<handleIndexBits - 1
	handleGenMask   = 1<<(32-handleIndexBits) - 1
)

type handleSlot[T comparable] struct {
	value T
	gen   uint32
	live  bool
}

type handleTable[T comparable] struct {
	slots   atomic.Pointer[[]handleSlot[T]]
	lastGen uint32 // guarded by mu
}

// open returns a new handle for value. Must be called with mu held.
func (t *handleTable[T]) open(value T) (uint32, error) {
	var s []handleSlot[T]
	if old := t.slots.Load(); old != nil {
		s = append(s, *old...)
	}
	index := -1
	for i := range s {
		if !s[i].live {
			index = i
			break
		}
	}
	if index < 0 {
		if len(s) >= handleIndexMask {
			return 0, fmt.Errorf("too many open fast-path handles")
		}
		s = append(s, handleSlot[T]{})
		index = len(s) - 1
	}
	t.lastGen = (t.lastGen + 1) & handleGenMask
	if t.lastGen == 0 {
		t.lastGen = 1
	}
	s[index] = handleSlot[T]{value: value, gen: t.lastGen, live: true}
	t.slots.Store(&s)
	return t.lastGen<<handleIndexBits | uint32(index+1), nil
}

// get resolves a handle without locking or allocating.
func (t *handleTable[T]) get(handle uint32) (T, bool) {
	var zero T
	s := t.slots.Load()
	index := int(handle & handleIndexMask)
	if s == nil || index == 0 || index > len(*s) {
		return zero, false
	}
	slot := &(*s)[index-1]
	if !slot.live || slot.gen != handle>>handleIndexBits {
		return zero, false
	}
	return slot.value, true
}

// release frees every slot holding value. Must be called with mu held.
func (t *handleTable[T]) release(value T) {
	old := t.slots.Load()
	if old == nil {
		return
	}
	s := append([]handleSlot[T](nil), *old...)
	changed := false
	for i := range s {
		if s[i].live && s[i].value == value {
			s[i] = handleSlot[T]{gen: s[i].gen}
			changed = true
		}
	}
	if changed {
		t.slots.Store(&s)
	}
}

func (t *handleTable[T]) clear() {
	t.slots.Store(nil)
}

var (
	x360Handles handleTable[*xbox360.Xbox360]
	ds4Handles  handleTable[*dualshock4.DualShock4]
	fastHandles handleTable[*deviceInfo]
)

// clearX360Handles drops all fast-path handles (all device types). Called
// from viiper_shutdown with the global mu held.
func clearX360Handles() {
	x360Handles.clear()
	ds4Handles.clear()
	fastHandles.clear()
}

// releaseFastHandles frees every fast-path handle that refers to info. Called
// from viiper_device_remove with the global mu held.
func releaseFastHandles(info *deviceInfo) {
	fastHandles.release(info)
	if xdev, ok := info.dev.(*xbox360.Xbox360); ok {
		x360Handles.release(xdev)
	}
	if ds4, ok := info.dev.(*dualshock4.DualShock4); ok {
		ds4Handles.release(ds4)
	}
}

// inputView returns a view of the caller's buffer, or false for a length the
// buffer cannot have. unsafe.Slice panics on a negative length, and a Go panic
// in an exported call aborts the host process.
func inputView(data *C.uint8_t, length C.int) ([]byte, bool) {
	if length < 0 || (data == nil && length != 0) {
		return nil, false
	}
	if length == 0 {
		return nil, true
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(data)), int(length)), true
}

// viiper_device_open_fast resolves any device into a fast-path handle for
// viiper_device_set_input_fast. Unlike the typed x360/ds4 variants it works
// for every device type: the submit call takes the same wire format as the
// generic viiper_device_set_input, but skips the global mutex, map lookup
// and buffer copy. Same lifetime rules as the typed handles.
//
//export viiper_device_open_fast
func viiper_device_open_fast(busID C.uint32_t, deviceID C.uint32_t, outHandle *C.uint32_t) (rc C.int) {
	defer recoverExport(&rc)
	mu.Lock()
	defer mu.Unlock()

	if server == nil {
		return setError(fmt.Errorf("not initialized"))
	}

	info, ok := devices[deviceKey{busID: uint32(busID), devID: uint32(deviceID)}]
	if !ok {
		return setError(fmt.Errorf("device %d-%d not found", busID, deviceID))
	}

	handle, err := fastHandles.open(info)
	if err != nil {
		return setError(err)
	}
	if outHandle != nil {
		*outHandle = C.uint32_t(handle)
	}
	return 0
}

// viiper_device_set_input_fast is the type-agnostic hot path: a zero-copy
// view of the caller's buffer is decoded via the same dispatch as the
// generic call. Never touches lastError; returns -1 for an invalid handle,
// -2 for a decode/apply error.
//
//export viiper_device_set_input_fast
func viiper_device_set_input_fast(handle C.uint32_t, data *C.uint8_t, length C.int) (rc C.int) {
	defer recoverExport(&rc)
	info, ok := fastHandles.get(uint32(handle))
	if !ok {
		return -1
	}
	buf, ok := inputView(data, length)
	if !ok {
		return -2
	}
	if err := applyInput(info, buf); err != nil {
		return -2
	}
	return 0
}

//export viiper_device_open_x360
func viiper_device_open_x360(busID C.uint32_t, deviceID C.uint32_t, outHandle *C.uint32_t) (rc C.int) {
	defer recoverExport(&rc)
	mu.Lock()
	defer mu.Unlock()

	if server == nil {
		return setError(fmt.Errorf("not initialized"))
	}

	info, ok := devices[deviceKey{busID: uint32(busID), devID: uint32(deviceID)}]
	if !ok {
		return setError(fmt.Errorf("device %d-%d not found", busID, deviceID))
	}
	xdev, ok := info.dev.(*xbox360.Xbox360)
	if !ok {
		return setError(fmt.Errorf("device %d-%d is %q, not xbox360", busID, deviceID, info.typeName))
	}

	handle, err := x360Handles.open(xdev)
	if err != nil {
		return setError(err)
	}
	if outHandle != nil {
		*outHandle = C.uint32_t(handle)
	}
	return 0
}

// viiper_device_set_input_x360 is the allocation-free hot path. It does not
// take the global mutex and therefore never touches lastError; it returns -1
// for an invalid handle and 0 on success.
//
//export viiper_device_set_input_x360
func viiper_device_set_input_x360(handle C.uint32_t, buttons C.uint32_t, lt C.uint8_t, rt C.uint8_t, lx C.int16_t, ly C.int16_t, rx C.int16_t, ry C.int16_t) (rc C.int) {
	defer recoverExport(&rc)
	xdev, ok := x360Handles.get(uint32(handle))
	if !ok {
		return -1
	}
	xdev.UpdateInputState(xbox360.InputState{
		Buttons: uint32(buttons),
		LT:      uint8(lt),
		RT:      uint8(rt),
		LX:      int16(lx),
		LY:      int16(ly),
		RX:      int16(rx),
		RY:      int16(ry),
	})
	return 0
}

//export viiper_device_open_ds4
func viiper_device_open_ds4(busID C.uint32_t, deviceID C.uint32_t, outHandle *C.uint32_t) (rc C.int) {
	defer recoverExport(&rc)
	mu.Lock()
	defer mu.Unlock()

	if server == nil {
		return setError(fmt.Errorf("not initialized"))
	}

	info, ok := devices[deviceKey{busID: uint32(busID), devID: uint32(deviceID)}]
	if !ok {
		return setError(fmt.Errorf("device %d-%d not found", busID, deviceID))
	}
	ds4, ok := info.dev.(*dualshock4.DualShock4)
	if !ok {
		return setError(fmt.Errorf("device %d-%d is %q, not dualshock4", busID, deviceID, info.typeName))
	}

	handle, err := ds4Handles.open(ds4)
	if err != nil {
		return setError(err)
	}
	if outHandle != nil {
		*outHandle = C.uint32_t(handle)
	}
	return 0
}

// viiper_device_set_input_ds4 is the ds4 hot path: same 31-byte wire format
// as the generic call, but no global mutex, no map lookup, and a zero-copy
// view of the caller's buffer (parsed before return, never retained). Like
// the x360 fast path it never touches lastError; returns -1 on an invalid
// handle, -2 on a malformed buffer.
//
//export viiper_device_set_input_ds4
func viiper_device_set_input_ds4(handle C.uint32_t, data *C.uint8_t, length C.int) (rc C.int) {
	defer recoverExport(&rc)
	ds4, ok := ds4Handles.get(uint32(handle))
	if !ok {
		return -1
	}
	buf, ok := inputView(data, length)
	if !ok {
		return -2
	}
	var state dualshock4.InputState
	if err := state.UnmarshalBinary(buf); err != nil {
		return -2
	}
	ds4.UpdateInputState(&state)
	return 0
}
