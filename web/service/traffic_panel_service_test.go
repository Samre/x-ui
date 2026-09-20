package service

// 流量统计面板的回归测试：覆盖速率换算（真实采样间隔）、落库失败回填、
// 分桶对齐与"小时/按天合计一致"。只依赖纯计算逻辑，不访问数据库。

import (
	"testing"
	"time"

	"x-ui/database/model"
	"x-ui/xray"
)

// 本地整点取整：非整点偏移时区（+5:45）也必须落在本地 10:00，而不是 UTC 整点
func TestBucketStartAlignsToLocalHour(t *testing.T) {
	loc := time.FixedZone("+0545", 5*3600+45*60)
	local := time.Date(2026, 9, 20, 10, 47, 33, 0, loc)
	got := bucketStart(local.Unix(), GranularityHour, loc)
	want := time.Date(2026, 9, 20, 10, 0, 0, 0, loc).Unix()
	if got != want {
		t.Fatalf("+0545 时区桶起点错误: got=%d want=%d", got, want)
	}

	// 整点偏移时区保持原行为
	loc8 := time.FixedZone("+0800", 8*3600)
	l8 := time.Date(2026, 9, 20, 10, 47, 33, 0, loc8)
	if got, want := bucketStart(l8.Unix(), GranularityHour, loc8), time.Date(2026, 9, 20, 10, 0, 0, 0, loc8).Unix(); got != want {
		t.Fatalf("+0800 时区桶起点错误: got=%d want=%d", got, want)
	}

	// 按天仍按本地零点
	day := bucketStart(l8.Unix(), GranularityDay, loc8)
	if want := time.Date(2026, 9, 20, 0, 0, 0, 0, loc8).Unix(); day != want {
		t.Fatalf("按天桶起点错误: got=%d want=%d", day, want)
	}
}

// 24h 主序列：窗口取整到当前小时，桶数必须正好 24，首桶是完整小时
func TestBucketizeHourWindow(t *testing.T) {
	loc := time.FixedZone("+0800", 8*3600)
	now := time.Date(2026, 9, 20, 15, 23, 45, 0, loc)
	end := now.Unix()
	days := 1
	lastBucket := bucketStart(end, GranularityHour, loc)
	start := lastBucket - int64(days*24-1)*3600

	rows := make([]*model.TrafficSnapshot, 0)
	for ts := start; ts < end; ts += 60 {
		rows = append(rows, &model.TrafficSnapshot{InboundTag: "n1", Up: 60, Down: 120, CreatedAt: ts})
	}
	buckets, accs := bucketize(rows, start, end, GranularityHour, loc)
	if len(buckets) != 24 {
		t.Fatalf("24h 桶数错误: got=%d want=24", len(buckets))
	}
	if buckets[0] != start {
		t.Fatalf("首桶未对齐窗口起点: got=%d want=%d", buckets[0], start)
	}
	if got := accs[buckets[0]].Up; got != 60*60 {
		t.Fatalf("首桶不是完整小时: got=%d want=%d", got, 60*60)
	}
	var total int64
	for _, b := range buckets {
		total += accs[b].Up
	}
	if want := int64(len(rows)) * 60; total != want {
		t.Fatalf("桶内合计与行合计不一致: got=%d want=%d", total, want)
	}
	if got := len(buckets); got > 24 {
		t.Fatalf("桶数超过窗口: %d", got)
	}
}

// 7d：小时序列与按天序列覆盖同一窗口，桶数分别为 160（6 天 + 当前小时）与 7，两边合计必须一致
func TestBucketizeSevenDayWindows(t *testing.T) {
	loc := time.FixedZone("+0800", 8*3600)
	now := time.Date(2026, 9, 20, 15, 23, 45, 0, loc)
	end := now.Unix()
	days := 7

	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	dailyStart := dayStart.AddDate(0, 0, -(days - 1)).Unix()
	start := dailyStart
	lastBucket := bucketStart(end, GranularityHour, loc)

	rows := make([]*model.TrafficSnapshot, 0)
	for ts := start; ts < end; ts += 60 {
		rows = append(rows, &model.TrafficSnapshot{InboundTag: "n1", Up: 60, Down: 0, CreatedAt: ts})
	}

	hourBuckets, hourAccs := bucketize(rows, start, end, GranularityHour, loc)
	wantHours := int((lastBucket-start)/3600) + 1
	if len(hourBuckets) != wantHours {
		t.Fatalf("7d 小时桶数错误: got=%d want=%d", len(hourBuckets), wantHours)
	}
	if hourBuckets[0] != start {
		t.Fatalf("小时序列首桶未对齐窗口起点: got=%d want=%d", hourBuckets[0], start)
	}
	if got := hourAccs[hourBuckets[0]].Up; got != 3600 {
		t.Fatalf("7d 首桶不是完整小时: got=%d", got)
	}
	var hourTotal int64
	for _, b := range hourBuckets {
		hourTotal += hourAccs[b].Up
	}

	dayBuckets, dayAccs := bucketize(rows, start, end, GranularityDay, loc)
	if len(dayBuckets) != 7 {
		t.Fatalf("7d 按天桶数错误: got=%d want=7", len(dayBuckets))
	}
	if dayBuckets[0] != dailyStart {
		t.Fatalf("按天首桶未对齐自然日: got=%d want=%d", dayBuckets[0], dailyStart)
	}
	// 首日是完整自然日：24 小时 × 60 分钟 × 60 字节
	if got := dayAccs[dayBuckets[0]].Up; got != 24*60*60 {
		t.Fatalf("7d 首日不是完整自然日: got=%d want=%d", got, 24*60*60)
	}
	var dayTotal int64
	for _, b := range dayBuckets {
		dayTotal += dayAccs[b].Up
	}
	// 关键一致性：小时序列合计与按天序列合计必须相等（否则 P1 与 P3 的"合计"会不一致）
	if hourTotal != dayTotal {
		t.Fatalf("小时/按天合计不一致: hour=%d day=%d", hourTotal, dayTotal)
	}
}

// 15d 按天窗口：15 个桶，窗口起点即自然日零点
func TestBucketizeFifteenDayWindow(t *testing.T) {
	loc := time.FixedZone("+0800", 8*3600)
	now := time.Date(2026, 9, 20, 15, 23, 45, 0, loc)
	end := now.Unix()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	start := dayStart.AddDate(0, 0, -14).Unix()

	rows := make([]*model.TrafficSnapshot, 0)
	for ts := start; ts < end; ts += 60 {
		rows = append(rows, &model.TrafficSnapshot{InboundTag: "n1", Up: 60, Down: 0, CreatedAt: ts})
	}
	buckets, _ := bucketize(rows, start, end, GranularityDay, loc)
	if len(buckets) != 15 {
		t.Fatalf("15d 桶数错误: got=%d want=15", len(buckets))
	}
	if buckets[0] != start {
		t.Fatalf("15d 首桶未对齐: got=%d want=%d", buckets[0], start)
	}
}

// 速率按真实采样间隔折算，而不是硬编码 10 秒
func TestRecordUsesRealInterval(t *testing.T) {
	s := &TrafficPanelService{pending: map[string]*nodeDelta{}}

	// 首个样本：无法得知真实间隔 → 标称 10 秒
	s.Record([]*xray.Traffic{{IsInbound: true, Tag: "n1", Up: 100, Down: 200}})
	if got := s.realtime[0].Nodes["n1"].Up; got != 10 {
		t.Fatalf("首个样本速率错误: got=%d want=10", got)
	}

	// 上次采样在 30 秒前：300 字节增量应折算成约 10 B/s（旧实现会得到 30）
	s.mutex.Lock()
	s.lastSampleAt = time.Now().Unix() - 30
	s.mutex.Unlock()
	s.Record([]*xray.Traffic{{IsInbound: true, Tag: "n1", Up: 300, Down: 0}})
	got := s.realtime[len(s.realtime)-1].Nodes["n1"].Up
	if got < 9 || got > 10 {
		t.Fatalf("30 秒间隔未按真实间隔折算: got=%d want≈10", got)
	}

	// 落库失败回填：pending 必须累加而不是覆盖
	s.mergeBack([]*model.TrafficSnapshot{{InboundTag: "n1", Up: 7, Down: 9}})
	s.mutex.Lock()
	delta := s.pending["n1"]
	s.mutex.Unlock()
	if delta == nil || delta.Up != 407 || delta.Down != 209 {
		t.Fatalf("回填后 pending 错误: %+v", delta)
	}

	// Xray 重启造成的采集空档：标记后按标称间隔折算，不跨空档放大
	s.MarkSampleGap()
	s.Record([]*xray.Traffic{{IsInbound: true, Tag: "n1", Up: 100, Down: 0}})
	if got := s.realtime[len(s.realtime)-1].Nodes["n1"].Up; got != 10 {
		t.Fatalf("空档后速率错误: got=%d want=10", got)
	}
}

// 实时缓冲按 30 分钟窗口裁剪，且至少保留最新样本
func TestTrimRealtimeWindowAndCap(t *testing.T) {
	s := &TrafficPanelService{pending: map[string]*nodeDelta{}}
	now := time.Now().Unix()
	for i := 5; i > 0; i-- {
		s.realtime = append(s.realtime, &RealtimeSample{Time: now - int64(realtimeMaxAge) - int64(10*i)})
	}
	s.realtime = append(s.realtime, &RealtimeSample{Time: now})
	s.trimRealtimeLocked(now)
	if len(s.realtime) != 1 || s.realtime[0].Time != now {
		t.Fatalf("窗口裁剪错误: len=%d", len(s.realtime))
	}

	// 全部样本都比窗口新时，走条数上限
	s.realtime = nil
	for i := 0; i < realtimeMaxSamples+20; i++ {
		s.realtime = append(s.realtime, &RealtimeSample{Time: now})
	}
	s.trimRealtimeLocked(now)
	if len(s.realtime) != realtimeMaxSamples {
		t.Fatalf("条数上限错误: got=%d want=%d", len(s.realtime), realtimeMaxSamples)
	}
}
