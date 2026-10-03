package xray

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"x-ui/util/common"

	"github.com/Workiva/go-datastructures/queue"
	statsservice "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
)

var trafficRegex = regexp.MustCompile("(inbound|outbound)>>>([^>]+)>>>traffic>>>(downlink|uplink)")

func GetBinaryName() string {
	return fmt.Sprintf("xray-%s-%s", runtime.GOOS, runtime.GOARCH)
}

func GetBinaryPath() string {
	return "bin/" + GetBinaryName()
}

func GetConfigPath() string {
	return "bin/config.json"
}

func GetGeositePath() string {
	return "bin/geosite.dat"
}

func GetGeoipPath() string {
	return "bin/geoip.dat"
}

func stopProcess(p *Process) {
	p.Stop()
}

type Process struct {
	*process
}

func NewProcess(xrayConfig *Config) *Process {
	p := &Process{newProcess(xrayConfig)}
	runtime.SetFinalizer(p, stopProcess)
	return p
}

type process struct {
	// mutex 保护下面所有可变字段。它们分别被 gRPC 读取方（GetTraffic/GetVersion）、
	// cmd.Run() 的等待 goroutine（写 exitErr）和 Stop()（读 cmd.Process）访问，
	// 而调用方分处 cron 任务与 HTTP 处理器两个 goroutine。
	mutex sync.RWMutex

	cmd *exec.Cmd

	version string
	apiPort int

	config  *Config
	lines   *queue.Queue
	exitErr error
}

func newProcess(config *Config) *process {
	return &process{
		version: "Unknown",
		config:  config,
		lines:   queue.New(100),
	}
}

func (p *process) IsRunning() bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	if p.cmd == nil || p.cmd.Process == nil {
		return false
	}
	if p.cmd.ProcessState == nil {
		return true
	}
	return false
}

func (p *process) GetErr() error {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	return p.exitErr
}

func (p *process) GetResult() string {
	p.mutex.RLock()
	exitErr := p.exitErr
	p.mutex.RUnlock()
	if p.lines.Empty() && exitErr != nil {
		return exitErr.Error()
	}
	items, _ := p.lines.TakeUntil(func(item interface{}) bool {
		return true
	})
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, item.(string))
	}
	return strings.Join(lines, "\n")
}

func (p *process) GetVersion() string {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	return p.version
}

func (p *Process) GetAPIPort() int {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	return p.apiPort
}

func (p *Process) GetConfig() *Config {
	// config 在 newProcess 之后不再改写，无需加锁
	return p.config
}

func (p *process) refreshAPIPort() {
	port := 0
	for _, inbound := range p.config.InboundConfigs {
		if inbound.Tag == "api" {
			port = inbound.Port
			break
		}
	}
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.apiPort = port
}

func (p *process) refreshVersion() {
	cmd := exec.Command(GetBinaryPath(), "-version")
	data, err := cmd.Output()
	version := "Unknown"
	if err == nil {
		datas := bytes.Split(data, []byte(" "))
		if len(datas) > 1 {
			version = string(datas[1])
		}
	}
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.version = version
}

func (p *process) Start() (err error) {
	// 这里不能用 p.IsRunning()：Go 的 sync.RWMutex 不可重入，
	// 而下面写 p.cmd 时会持有写锁，再取读锁会自锁死。
	p.mutex.RLock()
	alreadyRunning := p.cmd != nil && p.cmd.Process != nil && p.cmd.ProcessState == nil
	p.mutex.RUnlock()
	if alreadyRunning {
		return errors.New("xray is already running")
	}

	defer func() {
		if err != nil {
			p.mutex.Lock()
			p.exitErr = err
			p.mutex.Unlock()
		}
	}()

	data, err := json.MarshalIndent(p.config, "", "  ")
	if err != nil {
		return common.NewErrorf("生成 xray 配置文件失败: %v", err)
	}
	configPath := GetConfigPath()
	err = os.WriteFile(configPath, data, fs.ModePerm)
	if err != nil {
		return common.NewErrorf("写入配置文件失败: %v", err)
	}

	cmd := exec.Command(GetBinaryPath(), "-c", configPath)
	p.mutex.Lock()
	p.cmd = cmd
	p.mutex.Unlock()

	stdReader, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	errReader, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	go func() {
		defer func() {
			common.Recover("")
			stdReader.Close()
		}()
		reader := bufio.NewReaderSize(stdReader, 8192)
		for {
			line, _, err := reader.ReadLine()
			if err != nil {
				return
			}
			if p.lines.Len() >= 100 {
				p.lines.Get(1)
			}
			p.lines.Put(string(line))
		}
	}()

	go func() {
		defer func() {
			common.Recover("")
			errReader.Close()
		}()
		reader := bufio.NewReaderSize(errReader, 8192)
		for {
			line, _, err := reader.ReadLine()
			if err != nil {
				return
			}
			if p.lines.Len() >= 100 {
				p.lines.Get(1)
			}
			p.lines.Put(string(line))
		}
	}()

	go func() {
		err := cmd.Run()
		if err != nil {
			p.mutex.Lock()
			p.exitErr = err
			p.mutex.Unlock()
		}
	}()

	p.refreshVersion()
	p.refreshAPIPort()

	return nil
}

func (p *process) Stop() error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if p.cmd == nil || p.cmd.Process == nil || p.cmd.ProcessState != nil {
		return errors.New("xray is not running")
	}
	return p.cmd.Process.Kill()
}

func (p *process) GetTraffic(reset bool) ([]*Traffic, error) {
	p.mutex.RLock()
	apiPort := p.apiPort
	p.mutex.RUnlock()
	if apiPort == 0 {
		return nil, common.NewError("xray api port wrong:", apiPort)
	}
	conn, err := grpc.Dial(fmt.Sprintf("127.0.0.1:%v", apiPort), grpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client := statsservice.NewStatsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	request := &statsservice.QueryStatsRequest{
		Reset_: reset,
	}
	resp, err := client.QueryStats(ctx, request)
	if err != nil {
		return nil, err
	}
	// 键必须含方向：同一个 tag 的 inbound 与 outbound 是两条独立的统计项，
	// 只按 tag 归并会让后者的值覆盖前者，并把 IsInbound 标成最后一条的方向。
	type trafficKey struct {
		isInbound bool
		tag       string
	}
	tagTrafficMap := map[trafficKey]*Traffic{}
	traffics := make([]*Traffic, 0)
	for _, stat := range resp.GetStat() {
		matchs := trafficRegex.FindStringSubmatch(stat.Name)
		// 统计项由外部 Xray 进程提供，命名不保证符合本正则；
		// 不判空就直接取下标会以索引越界 panic 掉整个面板进程。
		if len(matchs) < 4 {
			continue
		}
		isInbound := matchs[1] == "inbound"
		tag := matchs[2]
		isDown := matchs[3] == "downlink"
		if tag == "api" {
			continue
		}
		key := trafficKey{isInbound: isInbound, tag: tag}
		traffic, ok := tagTrafficMap[key]
		if !ok {
			traffic = &Traffic{
				IsInbound: isInbound,
				Tag:       tag,
			}
			tagTrafficMap[key] = traffic
			traffics = append(traffics, traffic)
		}
		if isDown {
			traffic.Down = stat.Value
		} else {
			traffic.Up = stat.Value
		}
	}

	return traffics, nil
}
