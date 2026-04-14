package main

// 本文件：替换当前 reman 可执行文件（CLI: reman update；RPC: Reman.SelfUpgrade / SelfUpgradeBinary）。
// 与 Procfile 中子进程的 upgrade 不同，此处仅升级 supervisor 自身二进制。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// resolveSelfExecutableAndStaged 解析当前 reman 可执行文件绝对路径（含符号链接），
// 在同目录下生成用于 update 落盘的临时文件路径，以及带时间后缀的备份路径（与 doUpgrade 命名风格一致）。
func resolveSelfExecutableAndStaged() (exePath, stagedPath, backupPath string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", "", err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return "", "", "", err
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	stagedExt := ""
	if runtime.GOOS == "windows" {
		stagedExt = ".exe"
	}
	staged := filepath.Join(filepath.Dir(exe), fmt.Sprintf(".reman_update_%d%s", os.Getpid(), stagedExt))

	dir := filepath.Dir(exe)
	base := filepath.Base(exe)
	baseExt := filepath.Ext(base)
	stem := strings.TrimSuffix(base, baseExt)
	suffix := time.Now().Format("0102150405")
	var backup string
	if baseExt != "" {
		backup = filepath.Join(dir, fmt.Sprintf("%s_%s%s", stem, suffix, baseExt))
	} else {
		backup = filepath.Join(dir, fmt.Sprintf("%s_%s", stem, suffix))
	}
	return exe, staged, backup, nil
}

// openSelfUpgradeSourceReader 按来源返回可读流：http(s) 为响应 Body，否则为本地文件。
func openSelfUpgradeSourceReader(src string) (io.ReadCloser, error) {
	low := strings.ToLower(src)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		resp, err := http.Get(src)
		if err != nil {
			return nil, fmt.Errorf("download: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("download: HTTP %s", resp.Status)
		}
		return resp.Body, nil
	}

	abs, err := filepath.Abs(src)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("source is a directory: %s", abs)
	}
	return os.Open(abs)
}

// materializeSelfUpgradeSource 将来源（URL 或本地路径）拷贝到 stagedPath，供替换可执行文件使用。
func materializeSelfUpgradeSource(src string, stagedPath string) error {
	rc, err := openSelfUpgradeSourceReader(src)
	if err != nil {
		return err
	}
	defer rc.Close()

	w, err := os.OpenFile(stagedPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("create staged file: %w", err)
	}
	buf := make([]byte, 8192)
	_, copyErr := io.CopyBuffer(w, rc, buf)
	closeErr := w.Close()
	if copyErr != nil {
		return fmt.Errorf("write staged file: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close staged file: %w", closeErr)
	}
	return nil
}

// ReplaceSelfExecutable backs up the running reman binary and replaces it with stagedPath.
func ReplaceSelfExecutable(stagedPath string) (backupPath string, err error) {
	exePath, _, backupPath, err := resolveSelfExecutableAndStaged()
	if err != nil {
		return "", err
	}

	if filepath.Clean(stagedPath) == filepath.Clean(exePath) {
		return "", errors.New("staged path is the same as executable path")
	}

	if err = rename(exePath, backupPath); err != nil {
		return "", fmt.Errorf("backup current binary: %w", err)
	}
	if err = rename(stagedPath, exePath); err != nil {
		_ = rename(backupPath, exePath)
		return "", fmt.Errorf("install new binary: %w", err)
	}
	_ = os.Remove(stagedPath)
	_ = os.Chmod(exePath, 0755)
	return backupPath, nil
}

// RunSelfUpgrade replaces this reman's binary from a local path or HTTP(S) URL.
func RunSelfUpgrade(src string, restartService bool) (backup string, err error) {
	_, staged, _, err := resolveSelfExecutableAndStaged()
	if err != nil {
		return "", err
	}
	defer os.Remove(staged)

	if err = materializeSelfUpgradeSource(src, staged); err != nil {
		return "", err
	}
	backup, err = ReplaceSelfExecutable(staged)
	if err != nil {
		return "", err
	}
	if restartService {
		if rerr := restartSupervisorService(); rerr != nil {
			sysLogger.Printf("update: restart service: %v", rerr)
			return backup, fmt.Errorf("binary upgraded (backup %s) but service restart failed: %w", backup, rerr)
		}
	}
	return backup, nil
}

// SelfUpgrade RPC：服务端自升级。参数为可选 -restart，以及本地路径或 http(s) URL。
// 用户命令为 reman update / reman run update，仍通过此 RPC 名调用以保持兼容。
func (r *Reman) SelfUpgrade(args []string, ret *string) error {
	var restart bool
	for len(args) > 0 {
		if args[0] == "-restart" {
			restart = true
			args = args[1:]
			continue
		}
		break
	}
	if len(args) < 1 {
		return errors.New("usage: update [-restart] PATH_OR_URL")
	}
	backup, err := RunSelfUpgrade(args[0], restart)
	if err != nil {
		return err
	}
	*ret = fmt.Sprintf("Successfully upgraded reman, backup: %s", backup)
	return nil
}

// SelfUpgradeBinaryArgs：客户端将整包二进制推送到服务端（用于 reman run update 且目标为远程、源为本地文件）。
type SelfUpgradeBinaryArgs struct {
	Data    []byte
	JsonOut bool
	Restart bool
}

// SelfUpgradeBinary RPC：接收 Data 写入临时文件后执行与 SelfUpgrade 相同的替换逻辑。
func (r *Reman) SelfUpgradeBinary(args SelfUpgradeBinaryArgs, ret *string) (err error) {
	defer func() {
		if args.JsonOut {
			m := map[string]interface{}{"success": err == nil}
			if err != nil {
				m["error"] = err.Error()
			}
			raw, _ := json.Marshal(m)
			*ret = string(raw)
		}
	}()
	if len(args.Data) == 0 {
		return errors.New("empty binary payload")
	}
	_, staged, _, err := resolveSelfExecutableAndStaged()
	if err != nil {
		return err
	}
	if err = os.WriteFile(staged, args.Data, 0755); err != nil {
		return fmt.Errorf("write staged file: %w", err)
	}
	backup, err := ReplaceSelfExecutable(staged)
	if err != nil {
		return err
	}
	if args.Restart {
		if rerr := restartSupervisorService(); rerr != nil {
			sysLogger.Printf("SelfUpgradeBinary: restart service: %v", rerr)
			return fmt.Errorf("binary upgraded (backup %s) but service restart failed: %w", backup, rerr)
		}
	}
	if !args.JsonOut {
		*ret = fmt.Sprintf("Successfully upgraded reman, backup: %s", backup)
	}
	return nil
}
