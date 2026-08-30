package service

import (
	"sync"
	"time"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"
	"x-ui/xray"
)

const (
	// 实时缓冲容量,采样间隔 10s,180 条 ≈ 30 分钟窗口
	realtimeWindowSize = 180
	// 历史数据保留天数,需覆盖 15 天报表
	trafficRetentionDay = 16
	// XrayTrafficJob 的采样间隔(秒),与 web.go 中 cron 一致
	trafficSampleInterval = 10
)

// RealtimeSample 一次采样的各节点速率(字节/秒)
type RealtimeSample struct {
	Time  int64                `json:"time"`
	Nodes map[string]NodeSpeed `json:"nodes"`
	Up    int64                `json:"up"`
	Down  int64                `json:"down"`
}

type NodeSpeed struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// SeriesPoint 历史聚合后的一个时间桶
type SeriesPoint struct {
	Time int64 `json:"time"`
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// NodeSeries 单个节点的时间序列,Remark/Port 用于前端图例展示
type NodeSeries struct {
	Tag    string        `json:"tag"`
	Remark string        `json:"remark"`
	Port   int           `json:"port"`
	Points []SeriesPoint `json:"points"`
}

// HistoryResult 历史报表数据:各节点 + 整机汇总
type HistoryResult struct {
	Granularity string        `json:"granularity"`
	Start       int64         `json:"start"`
	End         int64         `json:"end"`
	Nodes       []NodeSeries  `json:"nodes"`
	Total       []SeriesPoint `json:"total"`
}

type trafficDelta struct {
	Up   int64
	Down int64
}

type TrafficRecorder struct {
	mutex       sync.Mutex
	pending     map[string]*trafficDelta
	recent      []RealtimeSample
	lastCleanup int64
	once        sync.Once
}

var trafficRecorder *TrafficRecorder

// GetTrafficRecorder 返回全局唯一实例,XrayTrafficJob 与控制器共享同一缓冲
func GetTrafficRecorder() *TrafficRecorder {
	if trafficRecorder == nil {
		trafficRecorder = &TrafficRecorder{
			pending: make(map[string]*trafficDelta),
		}
	}
	return trafficRecorder
}

// Record 接收 XrayTrafficJob 每 10 秒采到的增量:累加进待落库缓冲,同时记入实时环形缓冲
func (r *TrafficRecorder) Record(traffics []*xray.Traffic) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	now := time.Now().Unix()
	sample := RealtimeSample{
		Time:  now,
		Nodes: make(map[string]NodeSpeed),
	}
	for _, traffic := range traffics {
		if !traffic.IsInbound {
			continue
		}
		delta, ok := r.pending[traffic.Tag]
		if !ok {
			delta = &trafficDelta{}
			r.pending[traffic.Tag] = delta
		}
		delta.Up += traffic.Up
		delta.Down += traffic.Down
		sample.Nodes[traffic.Tag] = NodeSpeed{
			Up:   traffic.Up / trafficSampleInterval,
			Down: traffic.Down / trafficSampleInterval,
		}
		sample.Up += traffic.Up / trafficSampleInterval
		sample.Down += traffic.Down / trafficSampleInterval
	}
	r.recent = append(r.recent, sample)
	if len(r.recent) > realtimeWindowSize {
		r.recent = r.recent[len(r.recent)-realtimeWindowSize:]
	}
}

// Flush 将待落库的增量按 1 分钟粒度写入 TrafficLog,并每小时清理超出保留期的数据
func (r *TrafficRecorder) Flush() {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	now := time.Now().Unix()
	if len(r.pending) > 0 {
		rows := make([]model.TrafficLog, 0, len(r.pending))
		for tag, delta := range r.pending {
			rows = append(rows, model.TrafficLog{
				InboundTag: tag,
				Up:         delta.Up,
				Down:       delta.Down,
				CreatedAt:  now,
			})
		}
		err := database.GetDB().CreateInBatches(&rows, 100).Error
		if err != nil {
			logger.Warning("flush traffic log failed:", err)
		} else {
			r.pending = make(map[string]*trafficDelta)
		}
	}

	if now-r.lastCleanup >= 3600 {
		r.lastCleanup = now
		err := database.GetDB().
			Where("created_at < ?", now-int64(trafficRetentionDay)*86400).
			Delete(&model.TrafficLog{}).Error
		if err != nil {
			logger.Warning("cleanup traffic log failed:", err)
		}
	}
}

// GetRecent 返回实时缓冲的副本
func (r *TrafficRecorder) GetRecent() []RealtimeSample {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	recent := make([]RealtimeSample, len(r.recent))
	copy(recent, r.recent)
	return recent
}

// GetHistory 查询 [start, end) 区间内的快照并按粒度在内存中分桶聚合,
// 分桶用面板时区,避免 SQLite strftime 的 UTC 时区问题
func (r *TrafficRecorder) GetHistory(start int64, end int64, granularity string, loc *time.Location) (*HistoryResult, error) {
	db := database.GetDB()
	logs := make([]model.TrafficLog, 0)
	err := db.Model(&model.TrafficLog{}).
		Where("created_at >= ? AND created_at < ?", start, end).
		Find(&logs).Error
	if err != nil {
		return nil, err
	}

	// 生成连续时间桶,缺口补零,图表无需再处理断点
	bucketStarts := make([]int64, 0)
	{
		t := time.Unix(start, 0).In(loc)
		var bucket time.Time
		if granularity == "day" {
			bucket = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		} else {
			bucket = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc)
		}
		for bucket.Unix() < end {
			bucketStarts = append(bucketStarts, bucket.Unix())
			if granularity == "day" {
				bucket = bucket.AddDate(0, 0, 1)
			} else {
				bucket = bucket.Add(time.Hour)
			}
		}
	}
	bucketIndex := make(map[int64]int, len(bucketStarts))
	for i, s := range bucketStarts {
		bucketIndex[s] = i
	}

	truncate := func(sec int64) int64 {
		t := time.Unix(sec, 0).In(loc)
		if granularity == "day" {
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).Unix()
		}
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc).Unix()
	}

	type nodeAcc struct {
		points []SeriesPoint
	}
	nodeAccs := make(map[string]*nodeAcc)
	totalPoints := make([]SeriesPoint, len(bucketStarts))
	for i := range totalPoints {
		totalPoints[i].Time = bucketStarts[i]
	}

	for _, log := range logs {
		idx, ok := bucketIndex[truncate(log.CreatedAt)]
		if !ok {
			continue
		}
		acc, ok := nodeAccs[log.InboundTag]
		if !ok {
			acc = &nodeAcc{points: make([]SeriesPoint, len(bucketStarts))}
			for i := range acc.points {
				acc.points[i].Time = bucketStarts[i]
			}
			nodeAccs[log.InboundTag] = acc
		}
		acc.points[idx].Up += log.Up
		acc.points[idx].Down += log.Down
		totalPoints[idx].Up += log.Up
		totalPoints[idx].Down += log.Down
	}

	// tag 映射节点备注与端口,便于图例展示;已删除的节点保留 tag 原样显示
	inboundService := InboundService{}
	inbounds, err := inboundService.GetAllInbounds()
	if err != nil {
		logger.Warning("get inbounds for traffic history failed:", err)
	}
	remarks := make(map[string]*model.Inbound, len(inbounds))
	for i := range inbounds {
		remarks[inbounds[i].Tag] = inbounds[i]
	}

	result := &HistoryResult{
		Granularity: granularity,
		Start:       start,
		End:         end,
		Total:       totalPoints,
	}
	for tag, acc := range nodeAccs {
		node := NodeSeries{Tag: tag, Points: acc.points}
		if in := remarks[tag]; in != nil {
			node.Remark = in.Remark
			node.Port = in.Port
		}
		result.Nodes = append(result.Nodes, node)
	}
	return result, nil
}
