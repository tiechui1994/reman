package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"
	"github.com/joho/godotenv"
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

// version is the git tag at the time of build and is used to denote the
// binary's current version. This value is supplied as an ldflag at compile
// time by goreleaser (see .goreleaser.yml).
const (
	name     = "reman"
	version  = "0.3.16"
	revision = "HEAD"
)

func usage() {
	fmt.Fprint(os.Stderr, `Tasks:
  reman check                      # Show entries in Procfile
  reman help [TASK]                # Show this help
  reman run COMMAND [PROCESS...]   # Run a command
                                       start
                                       stop
                                       stop-all
                                       restart
                                       restart-all
                                       list
                                       status
                                       upgrade
                                       debug
  reman start [PROCESS]            # Start the application
  reman version                    # Display Reman version
Options:
`)
	flag.PrintDefaults()
	os.Exit(0)
}

type Restart string

const (
	RestartAlways    Restart = "always"
	RestartOnFailure Restart = "on-failure"
	RestartNever     Restart = "never"
)

// ProcInfo information structure.
type ProcInfo struct {
	Name              string            `toml:"name"`
	WorkDir           string            `toml:"work-dir"`
	CmdLine           string            `toml:"cmd-line"`
	Env               map[string]string `toml:"env,omitempty"`
	IsShow            bool              `toml:"is-show,omitempty"`
	Log               string            `toml:"log,omitempty"`
	Depend            []string          `toml:"depend,omitempty"`
	Restart           Restart           `toml:"restart,omitempty"` // "always", "on-failure", "never"
	RestartMaxRetries int               `toml:"restart-max-retries,omitempty"`
	Version           bool              `toml:"version,omitempty"`

	// True if we called stopProc to kill the process, in which case an
	// *os.ExitError is not the fault of the subprocess
	stoppedBySupervisor bool

	mu      sync.Mutex
	cond    *sync.Cond
	waitErr error
	cmd     *exec.Cmd

	startTime    time.Time
	reStartCount uint

	colorIndex  int
	service     bool
	serviceAttr ServiceAttr
	version     string
	noLog       bool
}

func (p *ProcInfo) ReadyStart() {
	if len(p.Depend) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, name := range p.Depend {
		if proc := findProc(name); proc != nil {
			wg.Add(1)
			go func(proc *ProcInfo) {
				ticker := time.NewTicker(time.Millisecond * 200)
				defer func() {
					time.Sleep(time.Second)
					wg.Done()
					ticker.Stop()
				}()
				for range ticker.C {
					if proc.cmd != nil && proc.cmd.Process != nil && proc.cmd.Process.Pid > 0 {
						return
					}
				}
			}(proc)
		}
	}
	wg.Wait()
}

type ProcManager struct {
	ExitOnError *bool       `toml:"exit-on-error,omitempty"`
	ExitOnStop  *bool       `toml:"exit-on-stop,omitempty"`
	Procs       []*ProcInfo `toml:"procs"`
	group       *ReuseWaitGroup
}

func (p *ProcManager) listen() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}

	var list sync.Map

	type watchCmd struct {
		proc *ProcInfo
		name string
		path string
	}

	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				sysLogger.Println("event:", event)

				var dir, name string
				switch event.Op {
				case fsnotify.Create:
					// 在 CREATE 下, name 总是最终的文件名
					dir, name = filepath.Split(event.Name)
					if val, ok := list.Load(dir); ok {
						watch := val.(*watchCmd)
						if name == watch.name {
							time.AfterFunc(time.Millisecond*800, func() {
								if watch.proc.cmd != nil {
									sysLogger.Printf("restart %v ....", watch.proc.Name)
									restartProc(watch.proc.Name)
								}
							})
						}
					}
				case fsnotify.Rename:
					// windows下: 当 RENAME 没有来源, Name 不可信
					if strings.Contains(event.String(), "←") {
						dir, name = filepath.Split(event.Name)
					}
					if val, ok := list.Load(dir); ok {
						watch := val.(*watchCmd)
						if name == watch.name {
							time.AfterFunc(time.Millisecond*500, func() {
								if watch.proc.cmd != nil {
									sysLogger.Printf("restart %v ....", watch.proc.Name)
									restartProc(watch.proc.Name)
								}
							})
						}
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				sysLogger.Println("error:", err)
			}
		}
	}()

	for _, cmd := range p.Procs {
		args, err := ParseCmdline(cmd.CmdLine)
		if err != nil {
			sysLogger.Printf("parse %s: %v", cmd.CmdLine, err)
			continue
		}
		cmdPath := args[0]
		if cmd.WorkDir != "" {
			cmdPath = filepath.Join(cmd.WorkDir, cmdPath)
		}

		dir, name := filepath.Split(cmdPath)
		err = watcher.Add(dir)
		if err != nil {
			sysLogger.Printf("add watch %s: %v", cmdPath, err)
			continue
		}

		list.Store(dir, &watchCmd{
			proc: cmd,
			path: dir,
			name: name,
		})
	}
}

var (
	procMux    sync.Mutex
	procConfig ProcManager
)

// filename of Procfile.
var procfile = flag.String("f", "Procfile.toml", "proc file")

// rpc port number.
var port = flag.Uint("p", defaultPort(), "port")

var startRPCServer = flag.Bool("rpc-server", true, "Start an RPC server listening on "+defaultAddr())

// base directory
var basedir = flag.String("basedir", "", "base directory")

// show timestamp in log
var logTime = flag.Bool("logtime", true, "show timestamp in log")

var serviceName = flag.String("service", "", "Windows Service name")

var maxProcNameLength = 0

var re = regexp.MustCompile(`\$([a-zA-Z]+[a-zA-Z0-9_]+)`)

type config struct {
	Procfile string `yaml:"procfile"`
	// Port for RPC server
	Port    uint   `yaml:"port"`
	BaseDir string `yaml:"basedir"`
	Args    []string
}

func readConfig() *config {
	var cfg config

	if flag.NArg() == 0 {
		usage()
	}

	cfg.Procfile = *procfile
	cfg.Port = *port
	cfg.BaseDir = *basedir
	cfg.Args = flag.Args()

	b, err := os.ReadFile(".reman")
	if err == nil {
		toml.Unmarshal(b, &cfg)
	}
	return &cfg
}

func boolPtr(v bool) *bool {
	return &v
}

// read Procfile and parse it.
func readProcfile(cfg *config) error {
	content, err := os.ReadFile(cfg.Procfile)
	if err != nil {
		return err
	}
	procMux.Lock()
	defer procMux.Unlock()

	var temp ProcManager
	err = toml.Unmarshal(content, &temp)
	if err != nil {
		return err
	}

	if temp.ExitOnError == nil {
		temp.ExitOnError = boolPtr(false)
	}
	if temp.ExitOnStop == nil {
		temp.ExitOnStop = boolPtr(true)
	}
	if len(temp.Procs) == 0 {
		return errors.New("no valid entry")
	}

	temp.group = NewReuseWaitGroup()

	index := 0
	for i := range temp.Procs {
		proc := temp.Procs[i]
		proc.CmdLine = strings.TrimSpace(proc.CmdLine)
		proc.Name = strings.TrimSpace(proc.Name)
		proc.WorkDir = strings.TrimSpace(proc.WorkDir)
		if proc.Name == "" || proc.CmdLine == "" {
			return fmt.Errorf("name or cmdline can not empty")
		}

		if proc.Log != "" {
			if !filepath.IsAbs(proc.Log) {
				proc.Log, _ = filepath.Abs(filepath.Join(proc.WorkDir, proc.Log))
			}
		}
		proc.Log = filepath.ToSlash(proc.Log)

		if proc.Restart == "" {
			proc.Restart = RestartOnFailure
			proc.RestartMaxRetries = 100
		}

		switch runtime.GOOS {
		case "windows":
			proc.CmdLine = re.ReplaceAllStringFunc(proc.CmdLine, func(s string) string {
				return "%" + s[1:] + "%"
			})
		}
		proc.service = *serviceName != ""
		if proc.service {
			proc.noLog = true
			proc.IsShow = false
		}
		proc.colorIndex = index
		proc.mu = sync.Mutex{}
		proc.cond = sync.NewCond(&proc.mu)
		if len(proc.Name) > maxProcNameLength {
			maxProcNameLength = len(proc.Name)
		}
		index = (index + 1) % len(colors)
	}

	procConfig = temp
	procConfig.listen()
	return nil
}

func defaultServer(serverPort uint) string {
	if s, ok := os.LookupEnv("REMAN_RPC_SERVER"); ok {
		return s
	}
	return fmt.Sprintf("127.0.0.1:%d", defaultPort())
}

func defaultAddr() string {
	if s, ok := os.LookupEnv("REMAN_RPC_ADDR"); ok {
		return s
	}
	return "0.0.0.0"
}

// default port
func defaultPort() uint {
	s := os.Getenv("REMAN_RPC_PORT")
	if s != "" {
		i, err := strconv.Atoi(s)
		if err == nil {
			return uint(i)
		}
	}
	return 18555
}

// command: check. show Procfile entries.
func check(cfg *config) error {
	err := readProcfile(cfg)
	if err != nil {
		return err
	}

	procMux.Lock()
	defer procMux.Unlock()

	keys := make([]string, len(procConfig.Procs))
	i := 0
	for _, proc := range procConfig.Procs {
		keys[i] = proc.Name
		i++
	}
	sort.Strings(keys)
	fmt.Printf("valid procfile detected (%s)\n", strings.Join(keys, ", "))
	return nil
}

func findProc(name string) *ProcInfo {
	procMux.Lock()
	defer procMux.Unlock()

	for _, proc := range procConfig.Procs {
		if proc.Name == name {
			return proc
		}
	}
	return nil
}

// command: start. spawn procs.
func start(ctx context.Context, sig <-chan os.Signal, cfg *config) error {
	err := readProcfile(cfg)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	// Cancel the RPC server when procs have returned/errored, cancel the
	// context anyway in case of early return.
	defer cancel()
	if len(cfg.Args) > 1 {
		tmpProcs := make([]*ProcInfo, 0, len(cfg.Args[1:]))
		maxProcNameLength = 0
		for _, v := range cfg.Args[1:] {
			proc := findProc(v)
			if proc == nil {
				return errors.New("unknown proc: " + v)
			}
			tmpProcs = append(tmpProcs, proc)
			if len(v) > maxProcNameLength {
				maxProcNameLength = len(v)
			}
		}
		procMux.Lock()
		procConfig.Procs = tmpProcs
		procMux.Unlock()
	}
	godotenv.Load()
	rpcChan := make(chan *rpcMessage, 10)
	if *startRPCServer {
		go startServer(ctx, rpcChan, cfg.Port)
	}
	return startProcs(sig, rpcChan)
}

func showVersion() {
	fmt.Fprintf(os.Stdout, "%s\n", version)
	os.Exit(0)
}

func main() {
	flag.Parse()
	if len(*serviceName) > 0 {
		asService, err := RunAsServiceIfNeeded(*serviceName)
		if err != nil {
			sysLogger.Printf("%s: %v", os.Args[0], err)
			os.Exit(1)
		}
		if asService {
			sysLogger.Println("main exit")
			os.Exit(0)
		}
	}

	var err error
	cfg := readConfig()

	if cfg.BaseDir != "" {
		err = os.Chdir(cfg.BaseDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reman: %s\n", err.Error())
			os.Exit(1)
		}
	}

	cmd := cfg.Args[0]
	switch cmd {
	case "check":
		err = check(cfg)
	case "help":
		usage()
	case "run":
		if len(cfg.Args) >= 2 {
			cmd, args := cfg.Args[1], cfg.Args[2:]
			err = run(cmd, args, cfg.Port)
		} else {
			usage()
		}
	case "start":
		c := notifyCh()
		err = start(context.Background(), c, cfg)
	case "version":
		showVersion()
	default:
		usage()
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", os.Args[0], err.Error())
		os.Exit(1)
	}
}
