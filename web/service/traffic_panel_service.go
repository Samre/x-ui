package service

import (
	"fmt"
	"sort"
	"sync"
	"time"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"
	"x-ui/xray"
)

// 实时环形缓冲：样本间隔 10 秒，最多 180 个 ≈ 最近 30 分钟
const realtimeSampleInterval = 10
const realtimeMaxSamples = 180

// 历史快照保留天数，需覆盖 15 天报表
const trafficRetentionDays = 16

// 主序列粒度：24小时/7天 按小时，15天 按天
const (
	GranularityHour = "hour"
	GranularityDay  = "day"
)

// 支持的报表范围
const (
	Range24h = "24h"
	Range7d  = "7d"
	Range15d = "15d"
)

type NodeSpeed struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// RealtimeSample 一个实时样本，Up/Down 单位为字节/秒
type RealtimeSample struct {
	Time  int64                 `json:"time"`
	Up    int64                 `json:"up"`
	Down  int64                 `json:"down"`
	Nodes map[string]*NodeSpeed `json:"nodes"`
}

type NodeMeta struct {
	Tag  string `json:"tag"`
	Name string `json:"name"`
}

type SeriesPoint struct {
	Time int64 `json:"time"`
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type NodeSeries struct {
	Tag    string        `json:"tag"`
	Name   string        `json:"name"`
	Points []SeriesPoint `json:"points"`
}

type TopItem struct {
	Tag   string `json:"tag"`
	Name  string `json:"name"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
	Total int64  `json:"total"`
}

// OverviewResult 一次返回指定范围的全套面板数据。
// 时间均为桶起始的 unix 秒，桶连续且零填充，前端按 time 对齐即可。
type OverviewResult struct {
	Range       string        `json:"range"`
	Granularity string        `json:"granularity"`
	Start       int64         `json:"start"`
	End         int64         `json:"end"`
	Total       []SeriesPoint `json:"total"`      // 整机主序列（24h/7d 按小时，15d 按天）
	Nodes       []NodeSeries  `json:"nodes"`      // 每节点主序列
	Daily       []SeriesPoint `json:"daily"`      // 整机按天
	DailyNodes  []NodeSeries  `json:"dailyNodes"` // 每节点按天（堆叠柱状用）
	Top         []TopItem     `json:"top"`        // 范围内流量 Top 10
	TotalUp     int64         `json:"totalUp"`
	TotalDown   int64         `json:"totalDown"`
	PeakUp      int64         `json:"peakUp"` // 主序列单桶平均速率峰值，字节/秒
	PeakDown    int64         `json:"peakDown"`
	NodeList    []NodeMeta    `json:"nodeList"`
}

type nodeDelta struct {
	Up   int64
	Down int64
}

type bucketAcc struct {
	Up    int64
	Down  int64
	Nodes map[string]*nodeDelta
}

type TrafficPanelService struct {
	settingService SettingService

	mutex    sync.Mutex
	pending  map[string]*nodeDelta // 距上次落库的各入站增量
	realtime []*RealtimeSample
}

var (
	trafficPanelOnce sync.Once
	trafficPanel     *TrafficPanelService
)

func GetTrafficPanelService() *TrafficPanelService {
	trafficPanelOnce.Do(func() {
		trafficPanel = &TrafficPanelService{
			pending: map[string]*nodeDelta{},
		}
	})
	return trafficPanel
}

// Record 由 XrayTrafficJob 每 10 秒调用，traffics 为自上次查询以来的增量
func (s *TrafficPanelService) Record(traffics []*xray.Traffic) {
	if len(traffics) == 0 {
		return
	}
	now := time.Now().Unix()
	sample := &RealtimeSample{
		Time:  now,
		Nodes: map[string]*NodeSpeed{},
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	for _, t := range traffics {
		if !t.IsInbound {
			continue
		}
		speed := &NodeSpeed{
			Up:   t.Up / realtimeSampleInterval,
			Down: t.Down / realtimeSampleInterval,
		}
		sample.Nodes[t.Tag] = speed
		sample.Up += speed.Up
		sample.Down += speed.Down

		delta, ok := s.pending[t.Tag]
		if !ok {
			delta = &nodeDelta{}
			s.pending[t.Tag] = delta
		}
		delta.Up += t.Up
		delta.Down += t.Down
	}
	s.realtime = append(s.realtime, sample)
	if len(s.realtime) > realtimeMaxSamples {
		s.realtime = s.realtime[len(s.realtime)-realtimeMaxSamples:]
	}
}

// Flush 把待落库增量写入 TrafficSnapshot，由 TrafficPersistJob 每分钟调用
func (s *TrafficPanelService) Flush() error {
	s.mutex.Lock()
	if len(s.pending) == 0 {
		s.mutex.Unlock()
		return nil
	}
	now := time.Now().Unix()
	snapshots := make([]*model.TrafficSnapshot, 0, len(s.pending))
	for tag, delta := range s.pending {
		if delta.Up == 0 && delta.Down == 0 {
			continue
		}
		snapshots = append(snapshots, &model.TrafficSnapshot{
			InboundTag: tag,
			Up:         delta.Up,
			Down:       delta.Down,
			CreatedAt:  now,
		})
	}
	s.pending = map[string]*nodeDelta{}
	s.mutex.Unlock()

	if len(snapshots) == 0 {
		return nil
	}
	err := database.GetDB().CreateInBatches(snapshots, 100).Error
	if err != nil {
		logger.Warning("flush traffic snapshot failed:", err)
	}
	return err
}

// Cleanup 删除超过保留期的历史快照
func (s *TrafficPanelService) Cleanup() error {
	cutoff := time.Now().Unix() - int64(trafficRetentionDays)*86400
	return database.GetDB().
		Where("created_at < ?", cutoff).
		Delete(&model.TrafficSnapshot{}).Error
}

// GetRealtime 返回实时缓冲的拷贝，样本写入后不再修改
func (s *TrafficPanelService) GetRealtime() []*RealtimeSample {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	samples := make([]*RealtimeSample, len(s.realtime))
	copy(samples, s.realtime)
	return samples
}

func (s *TrafficPanelService) getNodeMetas(inboundService *InboundService) []NodeMeta {
	metas := make([]NodeMeta, 0)
	inbounds, err := inboundService.GetAllInbounds()
	if err != nil {
		logger.Warning("get inbounds for traffic panel failed:", err)
		return metas
	}
	for _, inbound := range inbounds {
		name := inbound.Remark
		if name == "" {
			name = inbound.Tag
		} else {
			name = fmt.Sprintf("%v (%v)", inbound.Remark, inbound.Port)
		}
		metas = append(metas, NodeMeta{Tag: inbound.Tag, Name: name})
	}
	return metas
}

func bucketStart(t int64, granularity string, loc *time.Location) int64 {
	if granularity == GranularityDay {
		tm := time.Unix(t, 0).In(loc)
		return time.Date(tm.Year(), tm.Month(), tm.Day(), 0, 0, 0, 0, loc).Unix()
	}
	return t - t%3600
}

func nextBucketStart(t int64, granularity string, loc *time.Location) int64 {
	if granularity == GranularityDay {
		return time.Unix(t, 0).In(loc).AddDate(0, 0, 1).Unix()
	}
	return t + 3600
}

// bucketize 把快照按粒度聚合到连续桶，返回各桶累计与每节点累计
func bucketize(rows []*model.TrafficSnapshot, start, end int64, granularity string, loc *time.Location) ([]int64, map[int64]*bucketAcc) {
	buckets := make([]int64, 0)
	accs := map[int64]*bucketAcc{}
	for b := bucketStart(start, granularity, loc); b <= end; b = nextBucketStart(b, granularity, loc) {
		buckets = append(buckets, b)
		accs[b] = &bucketAcc{Nodes: map[string]*nodeDelta{}}
	}
	if len(buckets) == 0 {
		return buckets, accs
	}
	last := buckets[len(buckets)-1]
	for _, row := range rows {
		if row.CreatedAt < start || row.CreatedAt >= end {
			continue
		}
		b := bucketStart(row.CreatedAt, granularity, loc)
		if b > last {
			b = last
		}
		acc, ok := accs[b]
		if !ok {
			continue
		}
		acc.Up += row.Up
		acc.Down += row.Down
		delta, ok := acc.Nodes[row.InboundTag]
		if !ok {
			delta = &nodeDelta{}
			acc.Nodes[row.InboundTag] = delta
		}
		delta.Up += row.Up
		delta.Down += row.Down
	}
	return buckets, accs
}

func toSeries(buckets []int64, accs map[int64]*bucketAcc) []SeriesPoint {
	points := make([]SeriesPoint, len(buckets))
	for i, b := range buckets {
		p := SeriesPoint{Time: b}
		if acc, ok := accs[b]; ok {
			p.Up = acc.Up
			p.Down = acc.Down
		}
		points[i] = p
	}
	return points
}

func toNodeSeries(buckets []int64, accs map[int64]*bucketAcc, nameMap map[string]string, tags []string) []NodeSeries {
	series := make([]NodeSeries, 0, len(tags))
	for _, tag := range tags {
		ns := NodeSeries{Tag: tag, Name: nameMap[tag], Points: make([]SeriesPoint, len(buckets))}
		for i, b := range buckets {
			p := SeriesPoint{Time: b}
			if acc, ok := accs[b]; ok {
				if delta, ok := acc.Nodes[tag]; ok {
					p.Up = delta.Up
					p.Down = delta.Down
				}
			}
			ns.Points[i] = p
		}
		series = append(series, ns)
	}
	return series
}

func (s *TrafficPanelService) GetOverview(rangeKey string, inboundService *InboundService) (*OverviewResult, error) {
	loc, err := s.settingService.GetTimeLocation()
	if err != nil {
		loc = time.Local
	}

	granularity := GranularityHour
	var days int
	switch rangeKey {
	case Range24h:
		days = 1
	case Range15d:
		days = 15
		granularity = GranularityDay
	default:
		rangeKey = Range7d
		days = 7
	}

	now := time.Now().In(loc)
	end := now.Unix()
	var start int64
	if granularity == GranularityDay {
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		start = dayStart.AddDate(0, 0, -(days - 1)).Unix()
	} else {
		start = end - int64(days)*86400
	}

	db := database.GetDB()
	rows := make([]*model.TrafficSnapshot, 0)
	err = db.Model(&model.TrafficSnapshot{}).
		Where("created_at >= ? AND created_at < ?", start, end).
		Order("created_at asc").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	metas := s.getNodeMetas(inboundService)
	nameMap := map[string]string{}
	for _, meta := range metas {
		nameMap[meta.Tag] = meta.Name
	}

	// 主序列桶
	mainBuckets, mainAccs := bucketize(rows, start, end, granularity, loc)
	// 按天桶（P3 堆叠柱状），主序列已是按天时复用
	var dailyBuckets []int64
	var dailyAccs map[int64]*bucketAcc
	if granularity == GranularityDay {
		dailyBuckets, dailyAccs = mainBuckets, mainAccs
	} else {
		dailyBuckets, dailyAccs = bucketize(rows, start, end, GranularityDay, loc)
	}

	// 范围内每节点总量，用于 Top 排行与图例排序
	rangeAgg := map[string]*nodeDelta{}
	for _, acc := range mainAccs {
		for tag, delta := range acc.Nodes {
			d, ok := rangeAgg[tag]
			if !ok {
				d = &nodeDelta{}
				rangeAgg[tag] = d
			}
			d.Up += delta.Up
			d.Down += delta.Down
		}
	}
	tags := make([]string, 0, len(rangeAgg))
	for tag := range rangeAgg {
		tags = append(tags, tag)
	}
	sort.Slice(tags, func(i, j int) bool {
		ti := rangeAgg[tags[i]].Up + rangeAgg[tags[i]].Down
		tj := rangeAgg[tags[j]].Up + rangeAgg[tags[j]].Down
		return ti > tj
	})

	// 出现过但已被删除的入站也需要图例名称
	for _, row := range rows {
		if _, ok := nameMap[row.InboundTag]; !ok {
			nameMap[row.InboundTag] = row.InboundTag
		}
	}

	top := make([]TopItem, 0, len(tags))
	for i, tag := range tags {
		if i >= 10 {
			break
		}
		top = append(top, TopItem{
			Tag:   tag,
			Name:  nameMap[tag],
			Up:    rangeAgg[tag].Up,
			Down:  rangeAgg[tag].Down,
			Total: rangeAgg[tag].Up + rangeAgg[tag].Down,
		})
	}

	bucketSeconds := int64(3600)
	if granularity == GranularityDay {
		bucketSeconds = 86400
	}
	result := &OverviewResult{
		Range:       rangeKey,
		Granularity: granularity,
		Start:       start,
		End:         end,
		Total:       toSeries(mainBuckets, mainAccs),
		Nodes:       toNodeSeries(mainBuckets, mainAccs, nameMap, tags),
		Daily:       toSeries(dailyBuckets, dailyAccs),
		DailyNodes:  toNodeSeries(dailyBuckets, dailyAccs, nameMap, tags),
		Top:         top,
		NodeList:    metas,
	}
	for _, p := range result.Total {
		result.TotalUp += p.Up
		result.TotalDown += p.Down
		if p.Up/bucketSeconds > result.PeakUp {
			result.PeakUp = p.Up / bucketSeconds
		}
		if p.Down/bucketSeconds > result.PeakDown {
			result.PeakDown = p.Down / bucketSeconds
		}
	}
	return result, nil
}
