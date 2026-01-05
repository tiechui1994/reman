package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

type ServiceAttr struct {
	job windows.Handle
}

var (
	dll = windows.NewLazyDLL("kernel32.dll")

	procAttachConsole         = dll.NewProc("AttachConsole")
	procSetConsoleCtrlHandler = dll.NewProc("SetConsoleCtrlHandler")
)

var cmdStart = []string{"cmd", "/c"}
var ctrlHandeFunc uintptr

func sysProcAttr(opts ...Option) *syscall.SysProcAttr {
	var procAttrs = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP,
	}
	for _, opt := range opts {
		opt.apply(procAttrs)
	}

	return procAttrs
}

func WithConsole(console bool) Option {
	return newOption(func(p *syscall.SysProcAttr) {
		if console {
			p.HideWindow = false
			p.CreationFlags = p.CreationFlags | windows.CREATE_NEW_CONSOLE
		}
	})
}

func reuseListenConfig() *net.ListenConfig {
	return &net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var opErr error
			if err := c.Control(func(fd uintptr) {
				opErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
			}); err != nil {
				return err
			}
			return opErr
		},
	}
}

func ParseCmdline(cmdline string) ([]string, error) {
	ptr, err := syscall.UTF16PtrFromString(cmdline)
	if err != nil {
		return nil, err
	}

	var argc int32
	argv, err := windows.CommandLineToArgv(ptr, &argc)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(argv)))

	if argc == 0 {
		return []string{}, nil
	}

	args := make([]string, argc)
	start := unsafe.Pointer(argv)
	// 计算每个指针的大小 (在 64 位系统上是 8 字节)
	size := unsafe.Sizeof(uintptr(0))

	for i := 0; i < int(argc); i++ {
		// 计算第 i 个参数的指针地址
		p := *(**uint16)(unsafe.Pointer(uintptr(start) + uintptr(i)*size))
		args[i] = windows.UTF16PtrToString(p)
	}

	return args, nil
}

func (p *ProcInfo) terminateProcLock(signal os.Signal) error {
	if !p.service {
		return p.terminateAsCommon(signal)
	}

	return p.terminateAsService(signal)
}

func (p *ProcInfo) terminateAsCommon(signal os.Signal) error {
	var err error
	err = terminateProcWithConsoleEvent(p, signal)
	sysLogger.Println("common stopping step 1", p.Name, err)
	if err != nil {
		return err
	}

	timeout := time.AfterFunc(5*time.Second, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.cmd != nil {
			err = p.cmd.Process.Kill()
			sysLogger.Println("common stopping step 2", p.Name, err)
		}
	})
	p.cond.Wait()
	timeout.Stop()
	sysLogger.Println("common stopped", p.Name)
	return err
}

func (p *ProcInfo) terminateAsService(signal os.Signal) error {
	var err error
	err = p.cmd.Process.Kill()
	sysLogger.Println("service stopping step 1", p.Name, err)
	if err != nil {
		return err
	}

	timeout := time.AfterFunc(5*time.Second, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.cmd != nil {
			err = terminateJob(p.serviceAttr.job, 1)
			sysLogger.Println("service stopping step 2", p.Name, err)
		}
	})
	p.cond.Wait()
	timeout.Stop()
	sysLogger.Println("service stopped", p.Name)
	return err
}

func (p *ProcInfo) startHook() error {
	if p.service {
		handle, err := createJob()
		if err != nil {
			return err
		}
		err = assignProcessToJob(handle, p.cmd.Process.Pid)
		if err != nil {
			return err
		}
		p.serviceAttr.job = handle
	}
	return nil
}

func createJob() (windows.Handle, error) {
	// CreateJobObjectW
	return windows.CreateJobObject(nil, nil)
}

func assignProcessToJob(job windows.Handle, pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)

	// AssignProcessToJobObject
	return windows.AssignProcessToJobObject(job, h)
}

func terminateJob(job windows.Handle, exitCode uint32) error {
	// TerminateJobObject
	return windows.TerminateJobObject(job, exitCode)
}

func terminateProcWithConsoleEvent(proc *ProcInfo, _ os.Signal) error {
	pid := proc.cmd.Process.Pid

	r1, _, err := procAttachConsole.Call(uintptr(pid))
	if r1 == 0 && err != syscall.ERROR_ACCESS_DENIED {
		return err
	}

	r1, _, err = procSetConsoleCtrlHandler.Call(0, 1)
	if r1 == 0 {
		return err
	}

	err = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid))
	if err != nil {
		return err
	}
	err = windows.GenerateConsoleCtrlEvent(windows.CTRL_C_EVENT, uint32(pid))
	if err != nil {
		return err
	}
	return nil
}

func notifyCh() <-chan os.Signal {
	sc := make(chan os.Signal, 10)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGKILL)

	go func() {
		handlerPtr := syscall.NewCallback(ctrlHandler(sc))
		ctrlHandeFunc = handlerPtr
		runtime.KeepAlive(&ctrlHandeFunc)

		r1, _, err := procSetConsoleCtrlHandler.Call(handlerPtr, 1)
		if r1 == 0 {
			sysLogger.Println("SetConsoleCtrlHandler 调用失败:", err)
			return
		}
	}()

	return sc
}

type HandlerRoutine func(ctrlType uint32) uintptr

// 回调函数
func ctrlHandler(sc chan os.Signal) HandlerRoutine {
	return func(ctrlType uint32) uintptr {
		switch ctrlType {
		case windows.CTRL_C_EVENT: // CTRL_C_EVENT
			sc <- syscall.SIGKILL
			sysLogger.Println("收到 Ctrl+C")
		case windows.CTRL_BREAK_EVENT: // CTRL_BREAK_EVENT
			sc <- syscall.SIGKILL
			sysLogger.Println("收到 Ctrl+Break")
		case windows.CTRL_CLOSE_EVENT: // CTRL_CLOSE_EVENT
			sc <- syscall.SIGKILL
			sysLogger.Println("控制台窗口被关闭!")
		case windows.CTRL_SHUTDOWN_EVENT: // CTRL_SHUTDOWN_EVENT
			sc <- syscall.SIGKILL
			sysLogger.Println("系统正在关机!")
		default:
			sc <- syscall.SIGKILL
			sysLogger.Printf("收到未知信号: %d", ctrlType)
		}
		time.Sleep(5 * time.Second)
		return 1 // 返回1表示已处理
	}
}

// RunAsServiceIfNeeded checks if we are running under SCM. If yes, runs the service and returns (true, err).
// If not running as a service (interactive session), returns (false, nil).
func RunAsServiceIfNeeded(serviceName string) (bool, error) {
	// If interactive session, do nothing and return true.
	isIntSess, err := svc.IsWindowsService()
	if err != nil {
		return false, fmt.Errorf("IsWindowsService failed: %w", err)
	}
	if !isIntSess {
		return false, nil
	}

	// Running as service (non-interactive). Ensure event log source exists, then run.
	// Create/ensure event log source (ignore error if already exists)
	logName := serviceName
	if err := eventlog.InstallAsEventCreate(logName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		// If already exists, InstallAsEventCreate returns an error; ignore that specific case.
		// We'll try to open the eventlog anyway below.
	}

	elog, err := eventlog.Open(logName)
	if err == nil {
		defer elog.Close()
		msg := fmt.Sprintf("%s service starting", serviceName)
		sysLogger.Println(msg)
		elog.Info(1, msg)
	}

	// svc.Run will call our service handler Execute method and block until it exits.
	if err := svc.Run(serviceName, &serviceHandler{log: elog}); err != nil {
		msg := fmt.Sprintf("svc.Run failed: %v", err)
		sysLogger.Println(msg)
		if elog != nil {
			elog.Error(1, msg)
		}
		return true, err
	}

	msg := fmt.Sprintf("%s service stopped", serviceName)
	sysLogger.Println(msg)
	if elog != nil {
		elog.Info(1, msg)
	}
	return true, nil
}

// serviceHandler implements svc.Handler
type serviceHandler struct {
	log *eventlog.Log
}

func (h *serviceHandler) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	// Report starting
	s <- svc.Status{State: svc.StartPending}
	stopCh := make(chan os.Signal, 1)
	doneCh := make(chan error, 1)

	go func() {
		cfg := readConfig()
		cmd := cfg.Args[0]
		switch cmd {
		case "start":
			err := start(context.Background(), stopCh, cfg)
			doneCh <- err
		default:
			doneCh <- fmt.Errorf("命令 %v 不支持后台交互方式", cmd)
		}
	}()

	// Now report running
	s <- svc.Status{State: svc.Running, Accepts: accepted}

loop:
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				// Respond to interrogate
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				// start shutdown
				s <- svc.Status{State: svc.StopPending}
				// notify goroutine to stop
				close(stopCh)
				// wait for tasks to finish, but don't block forever
				select {
				case err := <-doneCh:
					if err != nil && h.log != nil {
						msg := fmt.Sprintf("service stop: manager returned error: %v", err)
						sysLogger.Println(msg)
						h.log.Error(1, msg)
					}
				case <-time.After(60 * time.Second):
					if h.log != nil {
						msg := "service stop timeout waiting for manager to exit; forcing stop"
						sysLogger.Println(msg)
						h.log.Warning(1, msg)
					}
				}
				break loop
			default:
				// ignore other commands
			}
		case err := <-doneCh:
			// The managed exited on its own; stop service
			if err != nil {
				msg := fmt.Sprintf("manager exited with error: %v", err)
				sysLogger.Println(msg)
				if h.log != nil {
					h.log.Error(1, msg)
				}
			}
			break loop
		}
	}

	// report stopped
	s <- svc.Status{State: svc.Stopped}
	return false, 0
}

// isElevated checks if the current process is running with administrator privileges
func isElevated() (bool, error) {
	// Open current process token
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return false, err
	}
	defer token.Close()

	return token.IsElevated(), nil
}

// InstallService installs reman as a Windows service
func InstallService(cfg *config) error {
	// Check if running with administrator privileges
	elevated, err := isElevated()
	if err != nil {
		return fmt.Errorf("failed to check administrator privileges: %w", err)
	}
	if !elevated {
		return fmt.Errorf("install command must be run as administrator (right-click and select 'Run as administrator')")
	}

	// Check if procfile exists
	procfilePath := cfg.Procfile
	if procfilePath == "" {
		procfilePath = *procfile
	}
	if procfilePath == "" {
		procfilePath = "Procfile.toml"
	}
	
	// Get absolute path of procfile
	procfileAbsPath, err := filepath.Abs(procfilePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path of procfile: %w", err)
	}
	
	// Check if procfile exists
	if _, err := os.Stat(procfileAbsPath); os.IsNotExist(err) {
		return fmt.Errorf("procfile does not exist: %s (please create the configuration file before installing the service)", procfileAbsPath)
	}
	
	// Update cfg.Procfile to use absolute path for validation
	cfg.Procfile = procfileAbsPath
	
	// Try to read and validate the procfile
	if err := readProcfile(cfg); err != nil {
		return fmt.Errorf("failed to validate procfile %s: %w", procfileAbsPath, err)
	}

	// Get the executable path
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Default service name
	svcName := "reman"
	if *serviceName != "" {
		svcName = *serviceName
	}

	// Build service arguments
	args := []string{"-service", svcName, "start"}
	if cfg.Procfile != "" {
		args = append(args, "-f", cfg.Procfile)
	}
	if cfg.Port != 0 {
		args = append(args, "-p", fmt.Sprintf("%d", cfg.Port))
	}
	if cfg.BaseDir != "" {
		args = append(args, "-basedir", cfg.BaseDir)
	}

	// Connect to Windows service manager
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	// Check if service already exists
	s, err := m.OpenService(svcName)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists", svcName)
	}

	// Create service configuration
	config := mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        mgr.StartAutomatic,
		ErrorControl:     mgr.ErrorNormal,
		DisplayName:      "reman",
		Description:      "Reman is a process manager for managing multiple processes",
		DelayedAutoStart: false,
	}

	// Create the service
	s, err = m.CreateService(svcName, exePath, config, args...)
	if err != nil {
		return fmt.Errorf("failed to create service: %w", err)
	}
	defer s.Close()

	// Set service recovery actions
	recoveryActions := []mgr.RecoveryAction{
		{
			Type:  mgr.ServiceRestart,
			Delay: 60 * time.Second,
		},
		{
			Type:  mgr.ServiceRestart,
			Delay: 60 * time.Second,
		},
		{
			Type:  mgr.NoAction,
			Delay: 0,
		},
	}
	err = s.SetRecoveryActions(recoveryActions, uint32(86400)) // 24 hours
	if err != nil {
		sysLogger.Printf("warning: failed to set recovery actions: %v", err)
	}

	// Create event log source
	logName := svcName
	if err := eventlog.InstallAsEventCreate(logName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		sysLogger.Printf("warning: failed to create event log source: %v", err)
	}

	fmt.Fprintf(os.Stdout, "Service %s installed successfully.\n", svcName)
	fmt.Fprintf(os.Stdout, "You can start it with: sc start %s\n", svcName)
	fmt.Fprintf(os.Stdout, "You can stop it with: sc stop %s\n", svcName)
	fmt.Fprintf(os.Stdout, "You can remove it with: sc delete %s\n", svcName)

	return nil
}

// UninstallService uninstalls reman Windows service
func UninstallService(cfg *config) error {
	// Check if running with administrator privileges
	elevated, err := isElevated()
	if err != nil {
		return fmt.Errorf("failed to check administrator privileges: %w", err)
	}
	if !elevated {
		return fmt.Errorf("uninstall command must be run as administrator (right-click and select 'Run as administrator')")
	}

	// Default service name
	svcName := "reman"
	if *serviceName != "" {
		svcName = *serviceName
	}

	// Connect to Windows service manager
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	// Open the service
	s, err := m.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("service %s does not exist", svcName)
	}
	defer s.Close()

	// Query service status
	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("failed to query service status: %w", err)
	}

	// Stop the service if it's running
	if status.State != svc.Stopped {
		fmt.Fprintf(os.Stdout, "Stopping service %s...\n", svcName)
		_, err = s.Control(svc.Stop)
		if err != nil {
			return fmt.Errorf("failed to stop service: %w", err)
		}

		// Wait for service to stop (max 30 seconds)
		timeout := time.Now().Add(30 * time.Second)
		for time.Now().Before(timeout) {
			status, err = s.Query()
			if err != nil {
				break
			}
			if status.State == svc.Stopped {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		if status.State != svc.Stopped {
			return fmt.Errorf("service did not stop within 30 seconds")
		}
		fmt.Fprintf(os.Stdout, "Service %s stopped.\n", svcName)
	}

	// Delete the service
	err = s.Delete()
	if err != nil {
		return fmt.Errorf("failed to delete service: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Service %s uninstalled successfully.\n", svcName)

	return nil
}
