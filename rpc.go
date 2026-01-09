package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "net/http/pprof"
)

func rename(oldPath, newPath string) error {
	err := os.Rename(oldPath, newPath)
	if err == nil {
		return err
	}

	w, err := os.OpenFile(newPath, os.O_SYNC|os.O_CREATE|os.O_RDWR, os.ModePerm)
	if err != nil {
		return err
	}
	defer w.Close()
	r, err := os.OpenFile(oldPath, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer r.Close()
	_, err = io.CopyBuffer(w, r, make([]byte, 8192))
	return err
}

// Reman is RPC server
type Reman struct {
	rpcChan chan<- *rpcMessage
}

type rpcMessage struct {
	Msg  string
	Args []string
	// sending error (if any) when the task completes
	ErrCh chan error
}

type UpgradeBinaryArgs struct {
	Name    string
	Data    []byte
	JsonOut bool
}

// Start do start
func (r *Reman) Start(args []string, ret *string) (err error) {
	var jsonOut bool
	if len(args) > 0 && args[0] == "-json" {
		jsonOut = true
		args = args[1:]
	}
	defer func() {
		if jsonOut {
			var result = make(map[string]interface{})
			result["success"] = err == nil
			if err != nil {
				result["error"] = err
			}
			raw, _ := json.Marshal(result)
			*ret = string(raw)
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
		}
	}()

	for _, arg := range args {
		if err = startProc(arg, nil); err != nil {
			break
		}
	}

	if err == nil && !jsonOut {
		*ret = fmt.Sprintf("Successfully started: %v", args)
	}

	return err
}

// Stop do stop
func (r *Reman) Stop(args []string, ret *string) (err error) {
	var jsonOut bool
	if len(args) > 0 && args[0] == "-json" {
		jsonOut = true
		args = args[1:]
	}
	defer func() {
		if jsonOut {
			var result = make(map[string]interface{})
			result["success"] = err == nil
			if err != nil {
				result["error"] = err
			}
			raw, _ := json.Marshal(result)
			*ret = string(raw)
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
		}
	}()

	for _, proc := range args {
		if err = stopProc(proc, nil); err != nil {
			break
		}
	}

	if err == nil && !jsonOut {
		*ret = fmt.Sprintf("Successfully stopped: %v", args)
	}

	return err
}

// StopAll do stop all
func (r *Reman) StopAll(args []string, ret *string) (err error) {
	var jsonOut bool
	if len(args) > 0 && args[0] == "-json" {
		jsonOut = true
		args = args[1:]
	}
	defer func() {
		if jsonOut {
			var result = make(map[string]interface{})
			result["success"] = err == nil
			if err != nil {
				result["error"] = err
			}
			raw, _ := json.Marshal(result)
			*ret = string(raw)
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
		}
	}()

	for _, proc := range procConfig.Procs {
		if err = stopProc(proc.Name, nil); err != nil {
			break
		}
	}

	if err == nil && !jsonOut {
		*ret = "All processes stopped successfully"
	}

	return err
}

// Restart do restart
func (r *Reman) Restart(args []string, ret *string) (err error) {
	var jsonOut bool
	if len(args) > 0 && args[0] == "-json" {
		jsonOut = true
		args = args[1:]
	}
	defer func() {
		if jsonOut {
			var result = make(map[string]interface{})
			result["success"] = err == nil
			if err != nil {
				result["error"] = err
			}
			raw, _ := json.Marshal(result)
			*ret = string(raw)
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
		}
	}()
	for _, arg := range args {
		if err = restartProc(arg); err != nil {
			break
		}
	}

	if err == nil && !jsonOut {
		*ret = fmt.Sprintf("Successfully restarted: %v", args)
	}

	return err
}

// RestartAll do restart all
func (r *Reman) RestartAll(args []string, ret *string) (err error) {
	var jsonOut bool
	if len(args) > 0 && args[0] == "-json" {
		jsonOut = true
		args = args[1:]
	}
	defer func() {
		if jsonOut {
			var result = make(map[string]interface{})
			result["success"] = err == nil
			if err != nil {
				result["error"] = err
			}
			raw, _ := json.Marshal(result)
			*ret = string(raw)
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
		}
	}()

	// 1. 先停止所有进程
	sysLogger.Println("RestartAll: stopping all processes")
	for _, proc := range procConfig.Procs {
		if stopErr := stopProc(proc.Name, nil); stopErr != nil {
			sysLogger.Printf("RestartAll: failed to stop %s: %v", proc.Name, stopErr)
			if err == nil {
				err = stopErr
			}
		}
	}

	// 2. 关闭旧的 watcher 以避免 goroutine 泄漏
	sysLogger.Println("RestartAll: closing old watcher")
	procMux.Lock()
	procConfig.closeWatcher()
	procMux.Unlock()

	// 3. 重新加载配置文件
	sysLogger.Println("RestartAll: reloading configuration file")
	// 创建临时 config 对象用于重新加载配置
	cfg := &config{
		Procfile: *procfile,
		Port:     *port,
		BaseDir:  *basedir,
		Args:     []string{"start"}, // 使用 start 命令来重新加载
	}

	// 重新加载配置文件
	if reloadErr := readProcfile(cfg); reloadErr != nil {
		sysLogger.Printf("RestartAll: failed to reload config file: %v", reloadErr)
		if err == nil {
			err = fmt.Errorf("failed to reload config file: %w", reloadErr)
		}
		return err
	}
	sysLogger.Println("RestartAll: configuration file reloaded successfully")

	// 4. 启动所有进程
	sysLogger.Println("RestartAll: starting all processes")
	for _, proc := range procConfig.Procs {
		if startErr := startProc(proc.Name, nil); startErr != nil {
			sysLogger.Printf("RestartAll: failed to start %s: %v", proc.Name, startErr)
			if err == nil {
				err = startErr
			}
		}
	}

	if err == nil {
		sysLogger.Println("RestartAll: all processes restarted successfully")
		if !jsonOut {
			*ret = "All processes restarted successfully"
		}
	}
	return err
}

// Upgrade
func (r *Reman) Upgrade(args []string, ret *string) (err error) {
	var jsonOut bool
	if len(args) > 0 && args[0] == "-json" {
		jsonOut = true
		args = args[1:]
	}
	defer func() {
		if jsonOut {
			var result = make(map[string]interface{})
			result["success"] = err == nil
			if err != nil {
				result["error"] = err
			}
			raw, _ := json.Marshal(result)
			*ret = string(raw)
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
		}
	}()

	if len(args) < 2 {
		return fmt.Errorf("args must NAME PATH")
	}

	name := args[0]
	path := args[1]

	proc := findProc(name)
	if proc == nil {
		err = errors.New("unknown proc: " + name)
		return err
	}
	if strings.HasPrefix(path, "http") {
		resp, err := http.Get(path)
		if err != nil {
			return fmt.Errorf("downalod file: %v", err)
		}
		defer resp.Body.Close()

		dir := proc.WorkDir
		if dir == "" {
			dir, _ = os.Getwd()
		}

		switch runtime.GOOS {
		case "windows":
			path = name + "_upgrade.exe"
		default:
			path = name + "_upgrade"
		}
		path = filepath.Join(dir, path)
		writer, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			return fmt.Errorf("create upgrade file: %v", err)
		}

		_, err = io.CopyBuffer(writer, resp.Body, make([]byte, 8192))
		writer.Close()
		if err != nil {
			return fmt.Errorf("copy file: %v", err)
		}
	}

	backupName, version, err := doUpgrade(proc, path)
	if err == nil && !jsonOut {
		if version != "" {
			*ret = fmt.Sprintf("Successfully upgraded %s to version %s, backup file: %s", name, version, backupName)
		} else {
			*ret = fmt.Sprintf("Successfully upgraded %s, backup file: %s", name, backupName)
		}
	}
	return err
}

func (r *Reman) UpgradeBinary(args UpgradeBinaryArgs, ret *string) (err error) {
	defer func() {
		if args.JsonOut {
			var result = make(map[string]interface{})
			result["success"] = err == nil
			if err != nil {
				result["error"] = err.Error()
			}
			raw, _ := json.Marshal(result)
			*ret = string(raw)
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
		}
	}()

	proc := findProc(args.Name)
	if proc == nil {
		return errors.New("unknown proc: " + args.Name)
	}

	dir := proc.WorkDir
	if dir == "" {
		dir, _ = os.Getwd()
	}

	var path string
	switch runtime.GOOS {
	case "windows":
		path = args.Name + "_upgrade.exe"
	default:
		path = args.Name + "_upgrade"
	}
	path = filepath.Join(dir, path)

	err = os.WriteFile(path, args.Data, 0755)
	if err != nil {
		return fmt.Errorf("write upgrade file: %v", err)
	}

	backupName, version, err := doUpgrade(proc, path)
	if err == nil && !args.JsonOut {
		if version != "" {
			*ret = fmt.Sprintf("Successfully upgraded %s to version %s, backup file: %s", args.Name, version, backupName)
		} else {
			*ret = fmt.Sprintf("Successfully upgraded %s, backup file: %s", args.Name, backupName)
		}
	}
	return err
}

func doUpgrade(proc *ProcInfo, path string) (string, string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", "", err
	}

	// 1. stop
	err := stopProc(proc.Name, nil)
	if err != nil {
		return "", "", err
	}

	// 2. rename
	if proc.WorkDir != "" {
		_ = os.Chdir(proc.WorkDir)
	}
	cmdArgs, err := ParseCmdline(proc.CmdLine)
	if err != nil {
		return "", "", err
	}
	cmdName := cmdArgs[0]

	var backupName string
	if index := strings.LastIndex(cmdName, "."); index >= 0 {
		name := cmdName[:index]
		backupName = strings.Replace(cmdName, name, fmt.Sprintf("%s_%s", name, time.Now().Format("0102150405")), 1)
	} else {
		name := cmdName
		backupName = strings.Replace(cmdName, name, fmt.Sprintf("%s_%s", name, time.Now().Format("0102150405")), 1)
	}
	err = rename(cmdName, backupName)
	if err != nil {
		return "", "", err
	}
	err = rename(path, cmdName)
	if err != nil {
		return "", "", err
	}
	defer os.Remove(path)

	// 3. start
	err = startProc(proc.Name, nil)
	if err != nil {
		return backupName, "", err
	}

	// Get new version if enabled
	var version string
	if proc.Version {
		_ = versionProc(proc.Name)
		version = strings.TrimSpace(proc.version)
	}

	return backupName, version, nil
}

func printTable(keys []string, values [][]string) string {
	maxLine := make([]int, len(keys))
	for idx, key := range keys {
		maxLine[idx] = len(key) + 3
	}
	for _, value := range values {
		for idx, val := range value {
			length := len(val) + 3
			if maxLine[idx] < length {
				maxLine[idx] = length
			}
		}
	}

	var sb strings.Builder
	for idx, key := range keys {
		sb.WriteString(fmt.Sprintf("%s%s", key, strings.Repeat(" ", maxLine[idx]-len(key))))
	}
	sb.WriteRune('\n')
	for idx := range keys {
		sb.WriteString(fmt.Sprintf("%s", strings.Repeat("-", maxLine[idx])))
	}
	sb.WriteRune('\n')
	for _, value := range values {
		for idx, val := range value {
			sb.WriteString(fmt.Sprintf("%s%s", val, strings.Repeat(" ", maxLine[idx]-len(val))))
		}
		sb.WriteRune('\n')
	}

	sb.WriteRune('\n')
	return sb.String()
}

// List do list
func (r *Reman) List(args []string, ret *string) (err error) {
	var keys = [7]string{"Name", "Status", "Time", "Restart", "LastError", "Version", "Log"}
	var values = make([][]string, 0, len(procConfig.Procs))

	var jsonOut bool
	if len(args) > 0 && args[0] == "-json" {
		jsonOut = true
		args = args[1:]
	}
	defer func() {
		if jsonOut {
			var result = make(map[string]interface{})
			result["success"] = err == nil
			if err != nil {
				result["error"] = err
			} else {
				list := make([]map[string]string, 0)
				for _, v := range values {
					list = append(list, map[string]string{
						"Name": v[0], "Status": v[1], "Time": v[2], "Restart": v[3],
						"LastError": v[4], "Version": v[5], "Log": v[6],
					})
				}
				result["data"] = list
			}
			raw, _ := json.Marshal(result)
			*ret = string(raw)
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
		}
	}()

	three := func(cond bool, trueValue func() string, falseValue func() string) string {
		if cond {
			if trueValue == nil {
				return ""
			}
			return trueValue()
		} else {
			if falseValue == nil {
				return ""
			}
			return falseValue()
		}
	}

	for _, proc := range procConfig.Procs {
		values = append(values, []string{
			proc.Name,
			three(proc.cmd != nil, func() string {
				return "Running"
			}, func() string {
				return "Dead"
			}),
			three(proc.cmd != nil, func() string {
				return time.Since(proc.startTime).String()
			}, nil),
			fmt.Sprintf("%d", proc.reStartCount),
			three(proc.waitErr != nil, func() string {
				return proc.waitErr.Error()
			}, nil),
			proc.version,
			proc.Log,
		})
	}
	*ret = printTable(keys[:], values)
	return nil
}

// Status do status
func (r *Reman) Status(args []string, ret *string) (err error) {
	var keys = [2]string{"Name", "Status"}
	var values = make([][]string, 0, len(procConfig.Procs))

	var jsonOut bool
	if len(args) > 0 && args[0] == "-json" {
		jsonOut = true
		args = args[1:]
	}
	defer func() {
		if jsonOut {
			var result = make(map[string]interface{})
			result["success"] = err == nil
			if err != nil {
				result["error"] = err
			} else {
				list := make([]map[string]string, 0)
				for _, v := range values {
					list = append(list, map[string]string{
						"Name": v[0], "Status": v[1],
					})
				}
				result["data"] = list
			}
			raw, _ := json.Marshal(result)
			*ret = string(raw)
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
		}
	}()

	three := func(cond bool, trueValue string, falseValue string) string {
		if cond {
			return trueValue
		} else {
			return falseValue
		}
	}

	for _, proc := range procConfig.Procs {
		values = append(values, []string{
			proc.Name,
			three(proc.cmd != nil, "Running", "Dead"),
		})
	}
	*ret = printTable(keys[:], values)
	return err
}

func (r *Reman) Debug(args []string, ret *string) (err error) {
	var ch = make(chan error, 1)
	addr := fmt.Sprintf("0.0.0.0:%d", rand.Int31n(65535-1024)+1024)
	go func() {
		server := http.Server{
			Addr:    addr,
			Handler: http.DefaultServeMux,
		}

		done := make(chan struct{})
		go func() {
			ch <- server.ListenAndServe()
			close(done)
		}()

		select {
		case <-time.After(time.Hour):
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
			defer cancel()
			_ = server.Shutdown(ctx)
		case <-done:
		}
	}()

	select {
	case err = <-ch:
		return err
	case <-time.After(5 * time.Second):
		*ret = fmt.Sprintf("debug pprof: http://%s", addr)
		return nil
	}
}

// command: run.
func run(cmd string, args []string, serverPort uint, host string) error {
	addr := host
	if addr == "" {
		addr = defaultServer(serverPort)
	} else {
		if !strings.Contains(addr, ":") {
			addr = fmt.Sprintf("%s:%d", addr, serverPort)
		}
	}
	client, err := rpc.Dial("tcp", addr)
	if err != nil {
		return err
	}
	defer client.Close()
	var ret string
	switch cmd {
	case "start":
		if len(args) == 0 {
			return errors.New("start command require NAME")
		}
		err = client.Call("Reman.Start", args, &ret)
		if err == nil {
			fmt.Println(ret)
		}
		return err
	case "stop":
		if len(args) == 0 {
			return errors.New("stop command require NAME")
		}
		err = client.Call("Reman.Stop", args, &ret)
		if err == nil {
			fmt.Println(ret)
		}
		return err
	case "restart":
		if len(args) == 0 {
			return errors.New("restart command require NAME")
		}
		err = client.Call("Reman.Restart", args, &ret)
		if err == nil {
			fmt.Println(ret)
		}
		return err
	case "stop-all":
		err = client.Call("Reman.StopAll", args, &ret)
		if err == nil {
			fmt.Println(ret)
		}
		return err
	case "restart-all":
		err = client.Call("Reman.RestartAll", args, &ret)
		if err == nil {
			fmt.Println(ret)
		}
		return err
	case "list":
		err = client.Call("Reman.List", args, &ret)
		fmt.Print(ret)
		return err
	case "status":
		err = client.Call("Reman.Status", args, &ret)
		fmt.Print(ret)
		return err
	case "upgrade":
		var jsonOut bool
		if len(args) > 0 && args[0] == "-json" {
			jsonOut = true
			args = args[1:]
		}

		if len(args) < 2 {
			return errors.New("upgrade command require NAME PATH")
		}

		isRemote := !strings.HasPrefix(addr, "127.0.0.1:") && !strings.HasPrefix(addr, "localhost:")
		if isRemote {
			path := args[1]
			if !strings.HasPrefix(path, "http") {
				info, err := os.Stat(path)
				if err != nil {
					return fmt.Errorf("local file error: %v", err)
				}
				if info.IsDir() {
					return fmt.Errorf("path is a directory: %s", path)
				}
				absPath, err := filepath.Abs(path)
				if err != nil {
					return fmt.Errorf("absolute path error: %v", err)
				}

				data, err := os.ReadFile(absPath)
				if err != nil {
					return fmt.Errorf("read file error: %v", err)
				}

				fmt.Printf("Pushing upgrade binary (%d bytes) to remote...\n", len(data))
				err = client.Call("Reman.UpgradeBinary", UpgradeBinaryArgs{
					Name:    args[0],
					Data:    data,
					JsonOut: jsonOut,
				}, &ret)
				if err == nil {
					if ret != "" {
						fmt.Println(ret)
					} else {
						fmt.Println("Successfully pushed and upgraded binary")
					}
				}
				return err
			}
		}
		if jsonOut {
			args = append([]string{"-json"}, args...)
		}
		err = client.Call("Reman.Upgrade", args, &ret)
		if err == nil {
			if ret != "" {
				fmt.Println(ret)
			} else {
				fmt.Println("Successfully upgraded")
			}
		}
		return err
	case "debug":
		err = client.Call("Reman.Debug", args, &ret)
		if err == nil {
			fmt.Println(ret)
		}
		return err
	}
	return errors.New("unknown command")
}

// start rpc server.
func startServer(ctx context.Context, rpcChan chan<- *rpcMessage, listenPort uint) error {
	gm := &Reman{
		rpcChan: rpcChan,
	}
	rpc.Register(gm)
	server, err := reuseListenConfig().Listen(ctx, "tcp", fmt.Sprintf("%s:%d", defaultAddr(), listenPort))
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var acceptingConns = true
	for acceptingConns {
		conns := make(chan net.Conn, 1)
		go func() {
			conn, err := server.Accept()
			if err != nil {
				return
			}
			conns <- conn
		}()
		select {
		case <-ctx.Done():
			acceptingConns = false
			break
		case client := <-conns: // server is not canceled.
			wg.Add(1)
			go func() {
				defer wg.Done()
				rpc.ServeConn(client)
			}()
		}
	}
	done := make(chan struct{}, 1)
	go func() {
		wg.Wait()
		done <- struct{}{}
	}()
	select {
	case <-done:
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("RPC server did not shut down in 10 seconds, quitting")
	}
}
