package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var sysLogger *log.Logger

func init() {
	cmd, _ := os.Executable()
	runPath := filepath.Join(filepath.Dir(cmd), "run.log")
	fd, err := os.OpenFile(runPath, os.O_CREATE|os.O_APPEND|os.O_RDWR|os.O_SYNC, os.ModePerm)
	if err != nil {
		os.Exit(1)
	}

	sysLogger = log.New(fd, "[man] ", log.Lshortfile|log.Ltime|log.Ldate)
}

type Option interface {
	apply(p *syscall.SysProcAttr)
}

type funcOption struct {
	fun func(p *syscall.SysProcAttr)
}

func newOption(fun func(p *syscall.SysProcAttr)) Option {
	return &funcOption{
		fun: fun,
	}
}

func (f *funcOption) apply(p *syscall.SysProcAttr) {
	f.fun(p)
}

type emptyWriter struct{}

func (n emptyWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

func versionProc(name string) error {
	proc := findProc(name)
	if proc.WorkDir != "" {
		_ = os.Chdir(proc.WorkDir)
	}
	args, err := ParseCmdline(proc.CmdLine)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cs := append(cmdStart, args[0], "-v")
	cmd := exec.CommandContext(ctx, cs[0], cs[1:]...)
	cmd.Dir = proc.WorkDir

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err = cmd.Run()
	if err != nil {
		return err
	}

	proc.version = buf.String()
	return nil
}

// spawnProc starts the specified proc, and returns any error from running it.
func spawnProc(name string, errCh chan<- error) {
again:
	proc := findProc(name)
	proc.ReadyStart()
	if proc.WorkDir != "" {
		_ = os.Chdir(proc.WorkDir)
	}
	cs := append(cmdStart, proc.CmdLine)
	cmd := exec.Command(cs[0], cs[1:]...)
	cmd.SysProcAttr = sysProcAttr()
	cmd.Dir = proc.WorkDir
	cmd.Stdin = nil
	if proc.noLog {
		cmd.Stdout = emptyWriter{}
		cmd.Stderr = emptyWriter{}
	} else {
		logger := createLogger(name, proc.colorIndex)
		cmd.Stdout = logger
		cmd.Stderr = logger
	}
	if proc.IsShow {
		cmd.SysProcAttr = sysProcAttr(WithConsole(proc.IsShow))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	sysLogger.Printf("starting %s : %d ...", name, proc.reStartCount)
	if proc.Env == nil {
		proc.Env = make(map[string]string)
	}
	var env []string
	for k, v := range proc.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = append(os.Environ(), env...)
	// Start
	if err := cmd.Start(); err != nil {
		select {
		case errCh <- err:
		default:
		}
		sysLogger.Printf("Failed to start %s: %s", name, err)
		return
	}
	proc.cmd = cmd
	proc.startTime = time.Now()
	proc.stoppedBySupervisor = false
	err := proc.startHook()
	sysLogger.Printf("start hook %s: %v", name, err)

	// Wait
	proc.mu.Unlock()
	err = cmd.Wait()
	sysLogger.Printf("stopping %s ...", name)
	proc.mu.Lock()
	proc.cond.Broadcast()

	if !proc.stoppedBySupervisor {
		// Send msg to errCh
		if err != nil {
			select {
			case errCh <- err:
			default:
			}
		}

		// restart policy
		switch proc.Restart {
		case RestartAlways:
			sysLogger.Printf("wait %s always restart: %v", name, err)
			time.Sleep(time.Second)
			proc.reStartCount += 1
			goto again
		case RestartOnFailure:
			if proc.cmd.ProcessState.ExitCode() != 0 {
				sysLogger.Printf("wait %s on-failure restart: %v", name, err)
				time.Sleep(time.Second)
				proc.reStartCount += 1
				goto again
			}
		case RestartNever:
			sysLogger.Printf("wait %s never restart: %v", name, err)
		}
	}

	proc.waitErr = err
	proc.cmd = nil
	fmt.Fprintf(os.Stdout, "terminating %s\n", name)
}

// Stop the specified proc, issuing os.Kill if it does not terminate within 10
// seconds. If signal is nil, os.Interrupt is used.
func stopProc(name string, signal os.Signal) error {
	if signal == nil {
		signal = os.Interrupt
	}
	proc := findProc(name)
	if proc == nil {
		return errors.New("unknown proc: " + name)
	}

	proc.mu.Lock()
	defer proc.mu.Unlock()

	if proc.cmd == nil {
		return nil
	}
	proc.stoppedBySupervisor = true

	err := proc.terminateProcLock(signal)
	return err
}

// start specified proc. if proc is started already, return nil.
func startProc(name string, errCh chan<- error) error {
	proc := findProc(name)
	if proc == nil {
		return errors.New("unknown name: " + name)
	}

	proc.mu.Lock()
	if proc.cmd != nil {
		proc.mu.Unlock()
		return nil
	}

	procConfig.group.Add(1)
	go func() {
		defer func() {
			procConfig.group.Done()
			proc.mu.Unlock()
		}()

		if proc.Version {
			err := versionProc(name)
			if err != nil {
				return
			}
		}

		spawnProc(name, errCh)
	}()
	return nil
}

// restart specified proc.
func restartProc(name string) error {
	proc := findProc(name)
	if proc == nil {
		return errors.New("unknown proc: " + name)
	}
	reStartCount := proc.reStartCount

	err := stopProc(name, nil)
	if err != nil {
		return err
	}

	proc.reStartCount = reStartCount + 1
	return startProc(name, nil)
}

// stopProcs attempts to stop every running process and returns any non-nil
// error, if one exists. stopProcs will wait until all procs have had an
// opportunity to stop.
func stopProcs(sig os.Signal) error {
	var err error
	var wg sync.WaitGroup
	for idx := range procConfig.Procs {
		wg.Add(1)
		go func(proc *ProcInfo) {
			defer wg.Done()
			stopErr := stopProc(proc.Name, sig)
			if stopErr != nil {
				err = stopErr
			}
		}(procConfig.Procs[idx])
	}
	wg.Wait()
	return err
}

// spawn all procs.
func startProcs(sc <-chan os.Signal, rpcCh <-chan *rpcMessage) error {
	errCh := make(chan error, 1)

	for _, proc := range procConfig.Procs {
		startProc(proc.Name, errCh)
	}

	allProcsDone := make(chan struct{}, 1)
	if *procConfig.ExitOnStop {
		go func() {
			procConfig.group.Wait()
			allProcsDone <- struct{}{}
		}()
	}
	for {
		select {
		case rpcMsg := <-rpcCh:
			// TODO: add more events here.
			switch rpcMsg.Msg {
			default:
				panic("unimplemented rpc message type " + rpcMsg.Msg)
			}
		case err := <-errCh:
			if *procConfig.ExitOnError {
				sysLogger.Printf("exit on error: %v", err)
				stopProcs(os.Interrupt)
				return err
			}
		case <-allProcsDone:
			sysLogger.Printf("exit on stop, all procs stopping")
			return stopProcs(os.Interrupt)
		case sig := <-sc:
			sysLogger.Printf("stop procs: %v", sig)
			return stopProcs(sig)
		}
	}
}
