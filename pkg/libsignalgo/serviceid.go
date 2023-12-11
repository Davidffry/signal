package libsignalgo

/*
#cgo LDFLAGS: -lsignal_ffi -ldl
#include "./libsignal-ffi.h"
#include <stdlib.h>
*/
import "C"
import (
	"unsafe"
)

type UUID [C.SignalUUID_LEN]byte

// func SignalServiceIdFromUUID(uuid UUID) (*C.SignalServiceIdFixedWidthBinaryBytes, error) {
// The function signature should be as above, but we must hack around a gcc bug, not needed for clang
// https://github.com/golang/go/issues/7270
func SignalServiceIdFromUUID(uuid UUID) (*[17]C.uint8_t, error) {
	var result C.SignalServiceIdFixedWidthBinaryBytes
	signalFfiError := C.signal_service_id_parse_from_service_id_binary(&result, BytesToBuffer(uuid[:]))
	if signalFfiError != nil {
		return nil, wrapError(signalFfiError)
	}
	return (*[17]C.uint8_t)(unsafe.Pointer(&result)), nil
}

func SignalPNIServiceIdFromUUID(uuid UUID) (*[17]C.uint8_t, error) {
	var result C.SignalServiceIdFixedWidthBinaryBytes
	// Prepend a 0x01 to the UUID to indicate that it is a PNI UUID
	pniUUID := append([]byte{0x01}, uuid[:]...)
	signalFfiError := C.signal_service_id_parse_from_service_id_binary(&result, BytesToBuffer(pniUUID))
	if signalFfiError != nil {
		return nil, wrapError(signalFfiError)
	}
	return (*[17]C.uint8_t)(unsafe.Pointer(&result)), nil
}

func SignalServiceIdToUUID(serviceId *C.SignalServiceIdFixedWidthBinaryBytes) (UUID, error) {
	result := C.SignalOwnedBuffer{}
	serviceIdBytes := (*[17]C.uchar)(unsafe.Pointer(serviceId)) // Hack around gcc bug, not needed for clang
	signalFfiError := C.signal_service_id_service_id_binary(&result, serviceIdBytes)
	if signalFfiError != nil {
		return UUID{}, wrapError(signalFfiError)
	}
	UUIDBytes := CopySignalOwnedBufferToBytes(result)
	var uuid UUID
	copy(uuid[:], UUIDBytes)
	return uuid, nil
}
