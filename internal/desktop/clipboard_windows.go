package desktop

import (
	"errors"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	openClipboard    = user32.NewProc("OpenClipboard")
	closeClipboard   = user32.NewProc("CloseClipboard")
	emptyClipboard   = user32.NewProc("EmptyClipboard")
	getClipboardData = user32.NewProc("GetClipboardData")
	setClipboardData = user32.NewProc("SetClipboardData")
	isFormatAvail    = user32.NewProc("IsClipboardFormatAvailable")
	globalAlloc      = kernel32.NewProc("GlobalAlloc")
	globalFree       = kernel32.NewProc("GlobalFree")
	globalLock       = kernel32.NewProc("GlobalLock")
	globalUnlock     = kernel32.NewProc("GlobalUnlock")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// Clipboard reads and writes the Windows clipboard (Unicode text).
type Clipboard struct{}

func NewClipboard() *Clipboard { return &Clipboard{} }

func open() error {
	for i := 0; i < 20; i++ {
		if r, _, _ := openClipboard.Call(0); r != 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("clipboard busy")
}

func (Clipboard) Read() (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, _ := isFormatAvail.Call(cfUnicodeText); r == 0 {
		return "", nil
	}
	if err := open(); err != nil {
		return "", err
	}
	defer closeClipboard.Call()
	h, _, _ := getClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", nil
	}
	p, _, _ := globalLock.Call(h)
	if p == 0 {
		return "", errors.New("lock failed")
	}
	defer globalUnlock.Call(h)
	var u []uint16
	for i := uintptr(0); i < 1<<21; i++ {
		c := *(*uint16)(unsafe.Pointer(p + i*2))
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return syscall.UTF16ToString(u), nil
}

func (Clipboard) Write(s string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return err
	}
	if err := open(); err != nil {
		return err
	}
	defer closeClipboard.Call()
	emptyClipboard.Call()
	n := uintptr(len(u) * 2)
	h, _, _ := globalAlloc.Call(gmemMoveable, n)
	if h == 0 {
		return errors.New("alloc failed")
	}
	p, _, _ := globalLock.Call(h)
	if p == 0 {
		globalFree.Call(h)
		return errors.New("lock failed")
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u)), u)
	globalUnlock.Call(h)
	if r, _, _ := setClipboardData.Call(cfUnicodeText, h); r == 0 {
		globalFree.Call(h)
		return errors.New("set failed")
	}
	return nil
}
