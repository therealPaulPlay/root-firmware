package metrics

import (
	"log"
	"os"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"

	"root-firmware/pkg/fsutil"
	"root-firmware/pkg/globals"
)

const (
	maxDataPoints      = 600              // 30 minutes at 3s intervals
	collectionInterval = 3 * time.Second  // Balance resolution vs resource usage
	saveInterval       = 30 * time.Second // Persist periodically, not every tick
)

type DataPoint struct {
	Timestamp   int64   `cbor:"t"`
	CPU         float64 `cbor:"cpu"`
	Memory      float64 `cbor:"mem"`
	Temperature float64 `cbor:"temp"`
	Disk        float64 `cbor:"disk"`
}

type collector struct {
	mu           sync.RWMutex
	points       []DataPoint
	prevCPUTimes cpu.TimesStat
}

var instance *collector

func Init() {
	os.MkdirAll(globals.FirmwareDataDir, 0755) // Ensure data dir exists
	instance = &collector{
		points: load(),
	}

	// Take initial CPU times reading for delta calculation
	if times, err := cpu.Times(false); err == nil && len(times) > 0 {
		instance.prevCPUTimes = times[0]
	}

	go instance.run()
}

func GetPoints() []DataPoint {
	if instance == nil {
		return nil
	}
	instance.mu.RLock()
	defer instance.mu.RUnlock()
	return append([]DataPoint{}, instance.points...)
}

func (c *collector) run() {
	ticker := time.NewTicker(collectionInterval)
	defer ticker.Stop()

	saveTicker := time.NewTicker(saveInterval)
	defer saveTicker.Stop()

	for {
		select {
		case <-ticker.C:
			dp := c.collect()
			c.mu.Lock()
			c.points = append(c.points, dp)
			if len(c.points) > maxDataPoints {
				c.points = c.points[len(c.points)-maxDataPoints:]
			}
			cutoffMs := time.Now().UTC().Add(-1 * time.Hour).UnixMilli()
			start := 0
			for start < len(c.points) && c.points[start].Timestamp < cutoffMs {
				start++
			}
			if start > 0 {
				c.points = c.points[start:]
			}
			c.mu.Unlock()
		case <-saveTicker.C:
			c.mu.RLock()
			save(c.points)
			c.mu.RUnlock()
		}
	}
}

func (c *collector) collect() DataPoint {
	dp := DataPoint{Timestamp: time.Now().UnixMilli()}

	// CPU: non-blocking delta calculation from /proc/stat
	if times, err := cpu.Times(false); err == nil && len(times) > 0 {
		cur := times[0]
		prev := c.prevCPUTimes

		totalDelta := (cur.User + cur.System + cur.Nice + cur.Iowait + cur.Irq + cur.Softirq + cur.Steal + cur.Idle) -
			(prev.User + prev.System + prev.Nice + prev.Iowait + prev.Irq + prev.Softirq + prev.Steal + prev.Idle)

		if totalDelta > 0 {
			idleDelta := cur.Idle - prev.Idle
			dp.CPU = (1.0 - idleDelta/totalDelta) * 100.0
		}

		c.prevCPUTimes = cur
	}

	// Memory
	if vmStat, err := mem.VirtualMemory(); err == nil {
		dp.Memory = vmStat.UsedPercent
	}

	// Temperature
	if temps, err := host.SensorsTemperatures(); err == nil {
		for _, t := range temps {
			if t.SensorKey == "cpu_thermal" || t.SensorKey == "coretemp" {
				dp.Temperature = t.Temperature
				break
			}
		}
	}

	// Disk
	if diskStat, err := disk.Usage(globals.DataDir); err == nil {
		dp.Disk = diskStat.UsedPercent
	}

	return dp
}

func load() []DataPoint {
	data, err := os.ReadFile(globals.MetricsPath)
	if err != nil {
		return []DataPoint{}
	}
	var points []DataPoint
	if err := cbor.Unmarshal(data, &points); err != nil {
		log.Printf("Metrics: Failed to parse metrics file: %v", err)
		return []DataPoint{}
	}
	return points
}

func save(points []DataPoint) {
	data, err := cbor.Marshal(points)
	if err != nil {
		log.Printf("Metrics: Failed to marshal metrics: %v", err)
		return
	}
	if err := fsutil.AtomicWrite(globals.MetricsPath, data, 0644); err != nil {
		log.Printf("Metrics: Failed to save metrics: %v", err)
	}
}
