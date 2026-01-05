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
	return newOption(func(p *syscall.SysProcAttr) {
		// POSIX 系统通常不需要特殊处理，但保留接口一致性
		// 如果需要在前台运行，可以设置 Foreground 等属性
		// 这里保持空实现，因为 POSIX 系统默认行为已满足需求
	})
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

func ParseCmdline(cmdline string) ([]string, error) {
	return Split(cmdline)
}

func (p *ProcInfo) startHook() error {
	// POSIX 系统不需要像 Windows 那样的 job 对象
	// 但可以在这里添加其他初始化逻辑，如设置进程组等
	if p.service {
		// 如果是服务模式，可以添加额外的初始化
		sysLogger.Printf("start hook for service %s", p.Name)
	}
	return nil
}

func (p *ProcInfo) terminateProcLock(signal os.Signal) error {
	// 保持与 Windows 版本一致的接口，虽然 POSIX 不需要区分 service 和 common
	// 但为了代码一致性，保留这个结构
	if !p.service {
		return p.terminateAsCommon(signal)
	}
	// POSIX 系统作为服务运行时，处理方式与普通进程相同
	return p.terminateAsCommon(signal)
}

func (p *ProcInfo) terminateAsCommon(signal os.Signal) error {
	var err error
	err = terminateProc(p, signal)
	sysLogger.Printf("common stopping step 1 %s: %v", p.Name, err)
	if err != nil {
		return err
	}

	timeout := time.AfterFunc(5*time.Second, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.cmd != nil {
			// killProc kills the proc with pid pid, as well as its children.
			err = unix.Kill(-1*p.cmd.Process.Pid, unix.SIGKILL)
			sysLogger.Printf("common stopping step 2 %s: %v", p.Name, err)
		}
	})
	p.cond.Wait()
	timeout.Stop()
	sysLogger.Printf("common stopped %s", p.Name)
	return err
}

func terminateProc(proc *ProcInfo, signal os.Signal) error {
	p := proc.cmd.Process
	if p == nil {
		sysLogger.Printf("terminateProc: process is nil for %s", proc.Name)
		return nil
	}

	pgid, err := unix.Getpgid(p.Pid)
	if err != nil {
		sysLogger.Printf("terminateProc: failed to get pgid for %s (pid %d): %v", proc.Name, p.Pid, err)
		return err
	}

	// use pid, ref: http://unix.stackexchange.com/questions/14815/process-descendants
	// 如果进程组 ID 等于进程 ID，说明这是进程组 leader，使用负 PID 来发送信号给整个进程组
	pid := p.Pid
	if pgid == p.Pid {
		pid = -1 * pid
	}

	target, err := os.FindProcess(pid)
	if err != nil {
		sysLogger.Printf("terminateProc: failed to find process %d for %s: %v", pid, proc.Name, err)
		return err
	}
	
	err = target.Signal(signal)
	if err != nil {
		sysLogger.Printf("terminateProc: failed to send signal %v to process %d for %s: %v", signal, pid, proc.Name, err)
		return err
	}
	
	sysLogger.Printf("terminateProc: sent signal %v to process %d (pgid %d) for %s", signal, pid, pgid, proc.Name)
	return nil
}

func notifyCh() <-chan os.Signal {
	sc := make(chan os.Signal, 10)
	// 注册常见的终止信号
	// SIGINT: Ctrl+C
	// SIGTERM: 终止信号（可被捕获和处理）
	// SIGHUP: 挂起信号（通常用于重新加载配置）
	// SIGQUIT: 退出信号（通常用于生成 core dump）
	// 注意：SIGKILL 不能被捕获，所以不需要注册
	signal.Notify(sc, unix.SIGINT, unix.SIGTERM, unix.SIGHUP, unix.SIGQUIT)
	return sc
}

func RunAsServiceIfNeeded(serviceName string) (bool, error) {
	return false, nil
}
