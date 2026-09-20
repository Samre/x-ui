package job

import (
	"time"
	"x-ui/logger"
	"x-ui/web/service"
)

// TrafficPersistJob 每 1 分钟把实时统计的增量落库，并每小时清理一次过期快照
type TrafficPersistJob struct {
	lastCleanup time.Time
}

func NewTrafficPersistJob() *TrafficPersistJob {
	return new(TrafficPersistJob)
}

func (j *TrafficPersistJob) Run() {
	panel := service.GetTrafficPanelService()
	// 落库失败不影响清理：失败原因（只读、磁盘满）不应连带让过期数据永不清理；
	// Flush 内部已记录告警并把增量回填重试
	_ = panel.Flush()
	if time.Since(j.lastCleanup) >= time.Hour {
		j.lastCleanup = time.Now()
		if err := panel.Cleanup(); err != nil {
			logger.Warning("cleanup traffic snapshot failed:", err)
		}
	}
}
