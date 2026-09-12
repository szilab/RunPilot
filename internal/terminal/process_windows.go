//go:build windows

package terminal

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func attachProcessTree(process *os.Process) (func() error, error) {
	if process == nil {
		return func() error { return nil }, nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
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
