//go:build windows

package winservice

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/szilab/RunPilot/internal/daemon"
)

const (
	Name        = "RunPilot"
	DisplayName = "RunPilot Process Manager"

	serviceWin32OwnProcess = 0x00000010
	serviceStopped         = 0x00000001
	serviceStartPending    = 0x00000002
	serviceStopPending     = 0x00000003
	serviceRunning         = 0x00000004
	serviceAcceptStop      = 0x00000001
	serviceAcceptShutdown  = 0x00000004
	serviceControlStop     = 0x00000001
	serviceControlShutdown = 0x00000005
)

var (
	advapi32                       = syscall.NewLazyDLL("advapi32.dll")
	procStartServiceCtrlDispatcher = advapi32.NewProc("StartServiceCtrlDispatcherW")
	procRegisterServiceCtrlHandler = advapi32.NewProc("RegisterServiceCtrlHandlerExW")
	procSetServiceStatus           = advapi32.NewProc("SetServiceStatus")
	serviceMainCallback            = syscall.NewCallback(serviceMain)
	controlHandlerCallback         = syscall.NewCallback(controlHandler)

	ctxMu     sync.Mutex
	activeCtx *serviceContext
)

type serviceTableEntry struct {
	serviceName *uint16
	serviceProc uintptr
}

type serviceStatus struct {
	serviceType             uint32
	currentState            uint32
	controlsAccepted        uint32
	win32ExitCode           uint32
	serviceSpecificExitCode uint32
	checkPoint              uint32
	waitHint                uint32
}

type serviceContext struct {
	dataDir string
	options daemon.Options
	handle  uintptr
	cancel  context.CancelFunc
	done    chan error
}

func Install(dataDir string, options ...daemon.Options) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	binPath := quoteSC(exe) + " service-run --data-dir " + quoteSC(dataDir)
	if len(options) > 0 {
		if options[0].Port != 0 {
			binPath += fmt.Sprintf(" --port %d", options[0].Port)
		}
		if options[0].BasePath != "" {
			binPath += " --base-path " + quoteSC(options[0].BasePath)
		}
	}
	if err := runSC("create", Name,
		"binPath=", binPath,
		"start=", "auto",
		"DisplayName=", DisplayName,
	); err != nil {
		return err
	}
	_ = runSC("description", Name, "Supervises background processes, scheduled jobs and local backup jobs.")
	return nil
}

func Uninstall() error { return runSC("delete", Name) }
func Start() error     { return runSC("start", Name) }
func Stop() error      { return runSC("stop", Name) }

func runSC(args ...string) error {
	out, err := exec.Command("sc.exe", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sc.exe %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func quoteSC(v string) string {
	return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
}

func Run(dataDir string, options ...daemon.Options) error {
	name, err := syscall.UTF16PtrFromString(Name)
	if err != nil {
		return err
	}
	ctxMu.Lock()
	scOptions := daemon.Options{}
	if len(options) > 0 {
		scOptions = options[0]
	}
	activeCtx = &serviceContext{dataDir: dataDir, options: scOptions}
	ctxMu.Unlock()
	defer func() {
		ctxMu.Lock()
		activeCtx = nil
		ctxMu.Unlock()
	}()

	table := []serviceTableEntry{
		{serviceName: name, serviceProc: serviceMainCallback},
		{serviceName: nil, serviceProc: 0},
	}
	r1, _, callErr := procStartServiceCtrlDispatcher.Call(uintptr(unsafe.Pointer(&table[0])))
	if r1 == 0 {
		return fmt.Errorf("StartServiceCtrlDispatcherW: %w", callErr)
	}
	return nil
}

func serviceMain(argc uintptr, argv uintptr) uintptr {
	ctxMu.Lock()
	sc := activeCtx
	ctxMu.Unlock()
	if sc == nil {
		return 0
	}

	name, _ := syscall.UTF16PtrFromString(Name)
	handle, _, err := procRegisterServiceCtrlHandler.Call(
		uintptr(unsafe.Pointer(name)),
		controlHandlerCallback,
		0,
	)
	if handle == 0 {
		_ = err
		return 0
	}
	sc.handle = handle
	setStatus(sc, serviceStartPending, 0, 3000)

	runCtx, cancel := context.WithCancel(context.Background())
	sc.cancel = cancel
	sc.done = make(chan error, 1)
	go func() { sc.done <- daemon.Run(runCtx, sc.dataDir, sc.options) }()

	setStatus(sc, serviceRunning, serviceAcceptStop|serviceAcceptShutdown, 0)
	errRun := <-sc.done
	cancel()
	if errRun != nil {
		setStopped(sc, 1)
	} else {
		setStopped(sc, 0)
	}
	return 0
}

func controlHandler(control, eventType uint32, eventData, context uintptr) uintptr {
	ctxMu.Lock()
	sc := activeCtx
	ctxMu.Unlock()
	if sc == nil {
		return 0
	}
	switch control {
	case serviceControlStop, serviceControlShutdown:
		setStatus(sc, serviceStopPending, 0, 12000)
		if sc.cancel != nil {
			sc.cancel()
		}
	}
	return 0
}

func setStopped(sc *serviceContext, exit uint32) {
	setStatusWithExit(sc, serviceStopped, 0, 0, exit)
}

func setStatus(sc *serviceContext, state, accepts, waitHint uint32) {
	setStatusWithExit(sc, state, accepts, waitHint, 0)
}

func setStatusWithExit(sc *serviceContext, state, accepts, waitHint, exit uint32) {
	status := serviceStatus{
		serviceType:      serviceWin32OwnProcess,
		currentState:     state,
		controlsAccepted: accepts,
		win32ExitCode:    exit,
		waitHint:         waitHint,
	}
	procSetServiceStatus.Call(sc.handle, uintptr(unsafe.Pointer(&status)))
}

var _ = time.Second
