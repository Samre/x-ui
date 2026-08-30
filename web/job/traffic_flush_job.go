package job

import (
	"x-ui/web/service"
)

// TrafficFlushJob 每分钟把流量增量快照写入数据库
type TrafficFlushJob struct {
}

func NewTrafficFlushJob() *TrafficFlushJob {
	return new(TrafficFlushJob)
}

func (j *TrafficFlushJob) Run() {
	service.GetTrafficRecorder().Flush()
}
