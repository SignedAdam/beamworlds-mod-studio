//go:build windows

package main

import (
	"encoding/base64"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

func protectSecret(value, _ string) (string, error) {
	if value == "" {
		return "", nil
	}
	inputBytes := []byte(value)
	input := windows.DataBlob{Size: uint32(len(inputBytes)), Data: &inputBytes[0]}
	var output windows.DataBlob
	if err := windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		return "", err
	}
	if output.Data == nil || output.Size == 0 {
		return "", errors.New("DPAPI returned an empty protected value")
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(output.Data))))
	protected := append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...)
	return base64.StdEncoding.EncodeToString(protected), nil
}

func unprotectSecret(value, _ string) (string, error) {
	if value == "" {
		return "", nil
	}
	protected, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	input := windows.DataBlob{Size: uint32(len(protected)), Data: &protected[0]}
	var output windows.DataBlob
	var description *uint16
	if err := windows.CryptUnprotectData(&input, &description, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		return "", err
	}
	if description != nil {
		defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(description))))
	}
	if output.Data == nil || output.Size == 0 {
		return "", errors.New("DPAPI returned an empty secret")
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(output.Data))))
	plain := append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...)
	return string(plain), nil
}
