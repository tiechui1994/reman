package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/rpc"
	"strings"
	"sync"
	"time"
)

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
	for _, proc := range procConfig.Procs {
		if err = restartProc(proc.Name); err != nil {
			break
		}
	}
	return err
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
	var keys = [5]string{"Name", "Status", "Time", "Restart", "LastError"}
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
						"Name": v[0], "Status": v[1], "Time": v[2], "Restart": v[3], "LastError": v[4],
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

// command: run.
func run(cmd string, args []string, serverPort uint) error {
	client, err := rpc.Dial("tcp", defaultServer(serverPort))
	if err != nil {
		return err
	}
	defer client.Close()
	var ret string
	switch cmd {
	case "start":
		return client.Call("Reman.Start", args, &ret)
	case "stop":
		return client.Call("Reman.Stop", args, &ret)
	case "stop-all":
		return client.Call("Reman.StopAll", args, &ret)
	case "restart":
		return client.Call("Reman.Restart", args, &ret)
	case "restart-all":
		return client.Call("Reman.RestartAll", args, &ret)
	case "list":
		err := client.Call("Reman.List", args, &ret)
		fmt.Print(ret)
		return err
	case "status":
		err := client.Call("Reman.Status", args, &ret)
		fmt.Print(ret)
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
