//go:build !windows
// +build !windows

package main

import (
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type ServiceAttr struct {
}

var cmdStart = []string{"/bin/sh", "-c"}

func sysProcAttr(opts ...Option) *syscall.SysProcAttr {
	var procAttrs = &syscall.SysProcAttr{
		Setpgid: true,
	}
	for _, opt := range opts {
		opt.apply(procAttrs)
	}

	return procAttrs
}

func WithConsole(console bool) Option {
	return newOption(func(p *syscall.SysProcAttr) {})
}

func reuseListenConfig() *net.ListenConfig {
	return &net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var opErr error
			if err := c.Control(func(fd uintptr) {
				opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
				opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			}); err != nil {
				return err
			}
			return opErr
		},
	}
}

func (p *ProcInfo) startHook() error {
	return nil
}

func (p *ProcInfo) terminateProcLock(signal os.Signal) error {
	var err error
	err = terminateProc(p, signal)
	if err != nil {
		return err
	}

	timeout := time.AfterFunc(5*time.Second, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.cmd != nil {
			// killProc kills the proc with pid pid, as well as its children.
			err = unix.Kill(-1*p.cmd.Process.Pid, unix.SIGKILL)
		}
	})
	p.cond.Wait()
	timeout.Stop()
	return err
}

func terminateProc(proc *ProcInfo, signal os.Signal) error {
	p := proc.cmd.Process
	if p == nil {
		return nil
	}

	pgid, err := unix.Getpgid(p.Pid)
	if err != nil {
		return err
	}

	// use pid, ref: http://unix.stackexchange.com/questions/14815/process-descendants
	pid := p.Pid
	if pgid == p.Pid {
		pid = -1 * pid
	}

	target, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return target.Signal(signal)
}

func notifyCh() <-chan os.Signal {
	sc := make(chan os.Signal, 10)
	signal.Notify(sc, unix.SIGINT, unix.SIGTERM, unix.SIGHUP, unix.SIGKILL)
	return sc
}

func RunAsServiceIfNeeded(serviceName string) (bool, error) {
	return false, nil
}
