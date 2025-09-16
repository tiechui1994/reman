package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
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
