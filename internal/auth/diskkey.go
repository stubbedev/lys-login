package auth

import (
	"bytes"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

const diskKeyName = "cryptsetup"

type diskPassphraseFunc func() ([]byte, error)

func diskPassphrase() ([]byte, error) {
	id, err := requestUserKey(diskKeyName)
	if errors.Is(err, unix.ENOKEY) || errors.Is(err, unix.EKEYEXPIRED) || errors.Is(err, unix.EKEYREVOKED) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up the %q key in the kernel keyring: %w", diskKeyName, err)
	}
	payload, err := readKey(id)
	if err != nil {
		return nil, fmt.Errorf("read the %q key %d from the kernel keyring: %w", diskKeyName, id, err)
	}
	return lastPassword(payload), nil
}

func requestUserKey(name string) (int, error) {
	keyType, err := unix.BytePtrFromString("user")
	if err != nil {
		return 0, err
	}
	description, err := unix.BytePtrFromString(name)
	if err != nil {
		return 0, err
	}
	id, _, errno := unix.Syscall6(unix.SYS_REQUEST_KEY, uintptr(unsafe.Pointer(keyType)), uintptr(unsafe.Pointer(description)), 0, 0, 0, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(id), nil
}

func readKey(id int) ([]byte, error) {
	size, err := unix.KeyctlBuffer(unix.KEYCTL_READ, id, nil, 0)
	if err != nil {
		return nil, err
	}
	for {
		buf := make([]byte, size)
		n, err := unix.KeyctlBuffer(unix.KEYCTL_READ, id, buf, 0)
		if err != nil {
			clear(buf)
			return nil, err
		}
		if n <= size {
			return buf[:n], nil
		}
		clear(buf)
		size = n
	}
}

func lastPassword(payload []byte) []byte {
	defer clear(payload)
	passwords := bytes.Split(payload, []byte{0})
	for i := len(passwords) - 1; i >= 0; i-- {
		if len(passwords[i]) > 0 {
			return bytes.Clone(passwords[i])
		}
	}
	return nil
}
