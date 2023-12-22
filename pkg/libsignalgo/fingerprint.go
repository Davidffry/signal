// mautrix-signal - A Matrix-signal puppeting bridge.
// Copyright (C) 2023 Sumner Evans
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package libsignalgo

/*
#cgo LDFLAGS: -lsignal_ffi -ldl
#include "./libsignal-ffi.h"
*/
import "C"
import "runtime"

type FingerprintVersion uint32

const (
	FingerprintVersionV1 FingerprintVersion = 1
	FingerprintVersionV2 FingerprintVersion = 2
)

type Fingerprint struct {
	ptr *C.SignalFingerprint
}

func wrapFingerprint(ptr *C.SignalFingerprint) *Fingerprint {
	fingerprint := &Fingerprint{ptr: ptr}
	runtime.SetFinalizer(fingerprint, (*Fingerprint).Destroy)
	return fingerprint
}

func NewFingerprint(iterations, version FingerprintVersion, localIdentifier []byte, localKey *PublicKey, remoteIdentifier []byte, remoteKey *PublicKey) (*Fingerprint, error) {
	var pa *C.SignalFingerprint
	signalFfiError := C.signal_fingerprint_new(&pa, C.uint32_t(iterations), C.uint32_t(version), BytesToBuffer(localIdentifier), localKey.ptr, BytesToBuffer(remoteIdentifier), remoteKey.ptr)
	if signalFfiError != nil {
		return nil, wrapError(signalFfiError)
	}
	return wrapFingerprint(pa), nil
}

func (f *Fingerprint) Clone() (*Fingerprint, error) {
	var cloned *C.SignalFingerprint
	signalFfiError := C.signal_fingerprint_clone(&cloned, f.ptr)
	if signalFfiError != nil {
		return nil, wrapError(signalFfiError)
	}
	return wrapFingerprint(cloned), nil
}

func (f *Fingerprint) Destroy() error {
	runtime.SetFinalizer(f, nil)
	return wrapError(C.signal_fingerprint_destroy(f.ptr))
}

func (f *Fingerprint) ScannableEncoding() ([]byte, error) {
	var scannableEncoding C.SignalOwnedBuffer = C.SignalOwnedBuffer{}
	signalFfiError := C.signal_fingerprint_scannable_encoding(&scannableEncoding, f.ptr)
	if signalFfiError != nil {
		return nil, wrapError(signalFfiError)
	}
	return CopySignalOwnedBufferToBytes(scannableEncoding), nil
}

func (f *Fingerprint) DisplayString() (string, error) {
	var displayString *C.char
	signalFfiError := C.signal_fingerprint_display_string(&displayString, f.ptr)
	if signalFfiError != nil {
		return "", wrapError(signalFfiError)
	}
	return CopyCStringToString(displayString), nil
}

func (f *Fingerprint) Compare(fingerprint1, fingerprint2 []byte) (bool, error) {
	var compare C.bool
	signalFfiError := C.signal_fingerprint_compare(&compare, BytesToBuffer(fingerprint1), BytesToBuffer(fingerprint2))
	if signalFfiError != nil {
		return false, wrapError(signalFfiError)
	}
	return bool(compare), nil
}
