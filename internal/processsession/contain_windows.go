//go:build windows

package processsession

import (
	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"
	"os"
	"unsafe"
)

func contain(process *os.Process) (func() error, error) {
	if process == nil {
		return func() error { return nil }, nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	err = windows.AssignProcessToJobObject(job, ph)
	windows.CloseHandle(ph)
	if err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	return func() error { return windows.CloseHandle(job) }, nil
}
func killTree(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Kill()
}
func graceful(process *os.Process, terminal pty.Pty) error {
	if process == nil {
		return nil
	}
	_, err := terminal.Write([]byte{3}) // ConPTY Ctrl-C interrupt.
	return err
}
