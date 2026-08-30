package job

import (
	"time"
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
	if err := panel.Flush(); err != nil {
		return
	}
	if time.Since(j.lastCleanup) >= time.Hour {
		j.lastCleanup = time.Now()
		if err := panel.Cleanup(); err != nil {
			return
		}
	}
}
