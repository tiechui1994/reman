package main

// 本文件：远程交互式 Shell（reman shell、reman run shell）。
// 流程：RPC ShellOpen 返回临时端口与令牌 → 客户端 TCP 连该端口并发送 64 字节令牌 →
// 服务端将连接桥接到本机 shell（POSIX: bash+PTY；Windows: PowerShell，见 proc_*.go）。

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/rpc"
	"os"
	"time"

	"golang.org/x/term"
)

// shellTokenLen 为随机字节数；hex 编码后长度为 64，与握手 ReadFull 一致。
const shellTokenLen = 32

func newShellToken() (string, error) {
	b := make([]byte, shellTokenLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// shellOpenReply is returned as JSON in *ret from ShellOpen.
type shellOpenReply struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// ShellOpen listens on an ephemeral TCP port (all interfaces), waits for one
// connection that sends the 64-byte token, then attaches a platform shell.
func (r *Reman) ShellOpen(args []string, ret *string) error {
	token, err := newShellToken()
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return err
	}
	ap, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		ln.Close()
		return errors.New("shell listener is not tcp")
	}
	port := ap.Port

	go func() {
		defer ln.Close()
		// 限制在超时内完成一次 Accept，避免协程长期占用监听。
		_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(120 * time.Second))
		conn, aerr := ln.Accept()
		if aerr != nil {
			sysLogger.Printf("shell accept: %v", aerr)
			return
		}
		defer conn.Close()
		// 令牌阶段设读超时，通过后清除以便长连接交互。
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		tokBuf := make([]byte, 64)
		if _, err := io.ReadFull(conn, tokBuf); err != nil {
			sysLogger.Printf("shell token read: %v", err)
			return
		}
		if string(tokBuf) != token {
			sysLogger.Println("shell: invalid token")
			return
		}
		_ = conn.SetDeadline(time.Time{})
		br := bufio.NewReader(conn)
		if err := bridgeShellSession(conn, br); err != nil {
			sysLogger.Printf("shell session: %v", err)
		}
	}()

	b, err := json.Marshal(shellOpenReply{Port: port, Token: token})
	if err != nil {
		ln.Close()
		return err
	}
	*ret = string(b)
	return nil
}

// runShellTerminalBridge 在本地 TTY 上进入 raw 模式，双向转发 stdin/stdout 与 conn。
func runShellTerminalBridge(conn net.Conn) error {
	fd := int(os.Stdin.Fd())
	var oldState *term.State
	if term.IsTerminal(fd) {
		var err error
		oldState, err = term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("terminal raw mode: %w", err)
		}
		defer func() { _ = term.Restore(fd, oldState) }()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(conn, os.Stdin)
		_ = conn.Close()
	}()
	_, _ = io.Copy(os.Stdout, conn)
	<-done
	return nil
}

// hostFromRPCAddr returns the host part of "host:port" or the original string.
func hostFromRPCAddr(rpcAddr string) string {
	h, _, err := net.SplitHostPort(rpcAddr)
	if err != nil {
		return rpcAddr
	}
	return h
}

// attachRemoteShell dials the shell port, sends the token, and relays stdio.
func attachRemoteShell(rpcAddr string, openJSON string) error {
	var info shellOpenReply
	if err := json.Unmarshal([]byte(openJSON), &info); err != nil {
		return fmt.Errorf("shell open reply: %w", err)
	}
	if info.Port <= 0 || len(info.Token) != 64 {
		return errors.New("invalid shell session info from server")
	}
	shellAddr := net.JoinHostPort(hostFromRPCAddr(rpcAddr), fmt.Sprintf("%d", info.Port))
	conn, err := net.Dial("tcp", shellAddr)
	if err != nil {
		return fmt.Errorf("dial shell: %w", err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, info.Token); err != nil {
		return err
	}
	return runShellTerminalBridge(conn)
}

// runShellWithRPC 先 RPC 获取端口与令牌，再 attachRemoteShell（供 reman run shell 使用）。
func runShellWithRPC(client *rpc.Client, rpcAddr string) error {
	var ret string
	if err := client.Call("Reman.ShellOpen", []string{}, &ret); err != nil {
		return err
	}
	return attachRemoteShell(rpcAddr, ret)
}
