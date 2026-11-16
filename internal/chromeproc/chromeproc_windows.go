package chromeproc

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Running(dir string) ([]Process, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	prefix := strings.TrimRight(dir, `\`) + `\`
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var out []Process
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		path, err := imagePath(e.ProcessID)
		if err != nil {
			continue // exited, or a system process we may not open
		}
		if len(path) > len(prefix) && strings.EqualFold(path[:len(prefix)], prefix) {
			out = append(out, Process{PID: e.ProcessID, Parent: e.ParentProcessID, Path: path})
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return out, nil
}

const (
	wmSysCommand = 0x112
	wsCaption    = 0x00C00000
	// idcExit is IDC_EXIT in Chromium's chrome_command_ids.h. Chrome takes
	// its commands by WM_SYSCOMMAND.
	idcExit = 34031
)

var (
	user32        = windows.NewLazySystemDLL("user32.dll")
	postMessage   = user32.NewProc("PostMessageW")
	getWindowLong = user32.NewProc("GetWindowLongW")
)

// Close tells Chrome running from dir to quit, as Exit in its menu does:
// closing its windows one at a time would leave only the last in the session
// that Chrome restores.
func Close(dir string) error {
	ps, err := Running(dir)
	if err != nil {
		return err
	}
	pids := map[uint32]bool{}
	for _, p := range ps {
		pids[p.PID] = true
	}
	closing.Lock()
	defer closing.Unlock()
	closing.pids, closing.denied = pids, false
	if err := windows.EnumWindows(visitWindow, nil); err != nil {
		return err
	}
	if closing.denied {
		return ErrElevated
	}
	return nil
}

// Go never frees a callback, and each closure takes one of a fixed number,
// so every Close shares one.
var (
	closing struct {
		sync.Mutex
		pids   map[uint32]bool
		denied bool
	}
	visitWindow = windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		var pid uint32
		if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err == nil && closing.pids[pid] && isBrowserWindow(hwnd) {
			if ok, _, err := postMessage.Call(uintptr(hwnd), wmSysCommand, idcExit, 0); ok == 0 && errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				closing.denied = true
			}
		}
		return 1
	})
)

func isBrowserWindow(hwnd windows.HWND) bool {
	if !windows.IsWindowVisible(hwnd) {
		return false
	}
	gwlStyle := int32(-16)
	style, _, _ := getWindowLong.Call(uintptr(hwnd), uintptr(gwlStyle))
	if style&wsCaption != wsCaption {
		return false
	}
	class := make([]uint16, 64)
	n, err := windows.GetClassName(hwnd, &class[0], int32(len(class)))
	return err == nil && strings.HasPrefix(windows.UTF16ToString(class[:n]), "Chrome_WidgetWin_")
}

func imagePath(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// waitLimit is MAXIMUM_WAIT_OBJECTS, the most handles one wait takes.
const waitLimit = 64

// Wait returns once no process runs from dir, or with ctx's error. It
// waits on the processes that none of the others started, such as Chrome's
// browser process, so that renderers coming and going don't wake it.
func Wait(ctx context.Context, dir string) error {
	cancel, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(cancel)
	stop := context.AfterFunc(ctx, func() { windows.SetEvent(cancel) })
	defer stop()
	for {
		ps, err := Running(dir)
		if err != nil {
			return err
		}
		if len(ps) == 0 {
			return nil
		}
		handles := []windows.Handle{cancel}
		for _, p := range roots(ps) {
			if len(handles) == waitLimit {
				break
			}
			if h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, p.PID); err == nil {
				handles = append(handles, h)
			}
		}
		// With nothing to wait on, as when the processes exited since the
		// scan or refuse to be opened, look again in a second.
		timeout := uint32(windows.INFINITE)
		if len(handles) == 1 {
			timeout = 1000
		}
		_, err = windows.WaitForMultipleObjects(handles, false, timeout)
		for _, h := range handles[1:] {
			windows.CloseHandle(h)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
	}
}

// roots returns the processes whose parent isn't among ps.
func roots(ps []Process) []Process {
	pids := map[uint32]bool{}
	for _, p := range ps {
		pids[p.PID] = true
	}
	var out []Process
	for _, p := range ps {
		if !pids[p.Parent] {
			out = append(out, p)
		}
	}
	return out
}

// CreateProcess fails with ERROR_ELEVATION_REQUIRED, without asking, when
// chrome.exe is set to run as administrator. Only then does Start go
// through the shell, which asks for consent as a double-click would. The
// usual launch stays on CreateProcess, as Chromium's and Chrome++'s do.
func needsElevation(err error) bool { return errors.Is(err, windows.ERROR_ELEVATION_REQUIRED) }

var shellOpen = func(exe string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// ShellExecute may hand the launch to shell extensions, which need COM.
	switch err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err {
	case nil, syscall.Errno(windows.S_FALSE):
		defer windows.CoUninitialize()
	}
	return windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(exe), nil,
		windows.StringToUTF16Ptr(filepath.Dir(exe)), windows.SW_SHOWNORMAL)
}
