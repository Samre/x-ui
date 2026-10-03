package service

import (
	"encoding/json"
	"errors"
	"sync"
	"x-ui/logger"
	"x-ui/xray"

	"go.uber.org/atomic"
)

// p 与 result 是进程级共享状态：RestartXray/StopXray 会替换或改写它们，
// 而 HTTP 处理器（/server/status）与每 10 秒的流量任务会读取它们。
// 全部访问都必须经 xrayLock：
//   - 读指针用 RLock，拿到 *xray.Process 后立即解锁，把 gRPC 等阻塞调用
//     放在锁外（Process 自身有锁，见 xray/process.go）；
//   - 替换/改写用 Lock。
var (
	xrayLock sync.RWMutex
	p        *xray.Process
	result   string
)

var isNeedXrayRestart atomic.Bool

type XrayService struct {
	inboundService InboundService
	settingService SettingService
}

func (s *XrayService) IsXrayRunning() bool {
	xrayLock.RLock()
	proc := p
	xrayLock.RUnlock()
	return proc != nil && proc.IsRunning()
}

func (s *XrayService) GetXrayErr() error {
	xrayLock.RLock()
	proc := p
	xrayLock.RUnlock()
	if proc == nil {
		return nil
	}
	return proc.GetErr()
}

func (s *XrayService) GetXrayResult() string {
	// 可能缓存 result，用写锁
	xrayLock.Lock()
	defer xrayLock.Unlock()

	if result != "" {
		return result
	}
	running := p != nil && p.IsRunning()
	if running {
		return ""
	}
	if p == nil {
		return ""
	}
	result = p.GetResult()
	return result
}

func (s *XrayService) GetXrayVersion() string {
	xrayLock.RLock()
	proc := p
	xrayLock.RUnlock()
	if proc == nil {
		return "Unknown"
	}
	return proc.GetVersion()
}

func (s *XrayService) GetXrayConfig() (*xray.Config, error) {
	templateConfig, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return nil, err
	}

	xrayConfig := &xray.Config{}
	err = json.Unmarshal([]byte(templateConfig), xrayConfig)
	if err != nil {
		return nil, err
	}

	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		if !inbound.Enable {
			continue
		}
		inboundConfig := inbound.GenXrayInboundConfig()
		xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, *inboundConfig)
	}
	return xrayConfig, nil
}

func (s *XrayService) GetXrayTraffic() ([]*xray.Traffic, error) {
	xrayLock.RLock()
	proc := p
	xrayLock.RUnlock()
	if proc == nil || !proc.IsRunning() {
		return nil, errors.New("xray is not running")
	}
	// 锁已释放：GetTraffic 是 gRPC 调用，不能占着 xrayLock，
	// 否则重启 xray 会被它阻塞住。与 Stop 的并发由 Process 自身的锁约束。
	return proc.GetTraffic(true)
}

func (s *XrayService) RestartXray(isForce bool) error {
	logger.Debug("restart xray, force:", isForce)

	xrayConfig, err := s.GetXrayConfig()
	if err != nil {
		return err
	}

	xrayLock.Lock()
	defer xrayLock.Unlock()

	if p != nil && p.IsRunning() {
		if !isForce && p.GetConfig().Equals(xrayConfig) {
			logger.Debug("not need to restart xray")
			return nil
		}
		p.Stop()
	}

	p = xray.NewProcess(xrayConfig)
	result = ""
	return p.Start()
}

func (s *XrayService) StopXray() error {
	xrayLock.Lock()
	defer xrayLock.Unlock()
	logger.Debug("stop xray")
	if p != nil && p.IsRunning() {
		return p.Stop()
	}
	return errors.New("xray is not running")
}

func (s *XrayService) SetToNeedRestart() {
	isNeedXrayRestart.Store(true)
}

func (s *XrayService) IsNeedRestartAndSetFalse() bool {
	return isNeedXrayRestart.CAS(true, false)
}
