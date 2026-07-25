package collector

import (
	"strings"

	"github.com/bcicen/ctop/models"
	api "github.com/fsouza/go-dockerclient"
)

// Docker collector
type Docker struct {
	models.Metrics
	id         string
	client     *api.Client
	running    bool
	stream     chan models.Metrics
	done       chan bool
	lastCpu    float64
	lastSysCpu float64
	hostNCPU   uint8
}

func NewDocker(client *api.Client, id string, hostNCPU uint8) *Docker {
	return &Docker{
		Metrics:  models.Metrics{},
		id:       id,
		client:   client,
		hostNCPU: hostNCPU,
	}
}

func (c *Docker) Start() {
	c.done = make(chan bool)
	c.stream = make(chan models.Metrics)
	stats := make(chan *api.Stats)

	go func() {
		opts := api.StatsOptions{
			ID:     c.id,
			Stats:  stats,
			Stream: true,
			Done:   c.done,
		}
		c.client.Stats(opts)
		c.running = false
	}()

	go func() {
		defer close(c.stream)
		for s := range stats {
			c.ReadCPU(s)
			c.ReadMem(s)
			c.ReadNet(s)
			c.ReadIO(s)
			c.stream <- c.Metrics
		}
		log.Infof("collector stopped for container: %s", c.id)
	}()

	c.running = true
	log.Infof("collector started for container: %s", c.id)
}

func (c *Docker) Running() bool {
	return c.running
}

func (c *Docker) Stream() chan models.Metrics {
	return c.stream
}

func (c *Docker) Logs() LogCollector {
	return NewDockerLogs(c.id, c.client)
}

// Stop collector
func (c *Docker) Stop() {
	c.running = false
	c.done <- true
}

func (c *Docker) ReadCPU(stats *api.Stats) {
	ncpus := uint8(stats.CPUStats.OnlineCPUs)
	if ncpus == 0 {
		ncpus = uint8(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if ncpus == 0 {
		// Podman's compat stats endpoint leaves online_cpus/percpu_usage empty
		// under cgroup v2, so fall back to the host's total CPU count.
		ncpus = c.hostNCPU
	}
	total := float64(stats.CPUStats.CPUUsage.TotalUsage)
	system := float64(stats.CPUStats.SystemCPUUsage)
	if system == 0 {
		// Podman's compat stats endpoint omits system_cpu_usage entirely
		// (confirmed: its cpu_stats has cpu_usage/online_cpus/throttling_data
		// but no system_cpu_usage key at all). That counter always advances
		// at a fixed ncpus-per-nanosecond rate regardless of load, so
		// wall-clock time times CPU count is an exact substitute, not just
		// an approximation.
		system = float64(stats.Read.UnixNano()) * float64(ncpus)
	}

	cpudiff := total - c.lastCpu
	syscpudiff := system - c.lastSysCpu

	c.NCpus = ncpus
	c.CPUUtil = percent(cpudiff, syscpudiff)
	c.lastCpu = total
	c.lastSysCpu = system
	c.Pids = int(stats.PidsStats.Current)
}

func (c *Docker) ReadMem(stats *api.Stats) {
	c.MemUsage = int64(stats.MemoryStats.Usage - stats.MemoryStats.Stats.Cache)
	c.MemLimit = int64(stats.MemoryStats.Limit)
	c.MemPercent = percent(float64(c.MemUsage), float64(c.MemLimit))
}

func (c *Docker) ReadNet(stats *api.Stats) {
	var rx, tx int64
	for _, network := range stats.Networks {
		rx += int64(network.RxBytes)
		tx += int64(network.TxBytes)
	}
	c.NetRx, c.NetTx = rx, tx
}

func (c *Docker) ReadIO(stats *api.Stats) {
	var read, write int64
	for _, blk := range stats.BlkioStats.IOServiceBytesRecursive {
		// Docker reports "Read"/"Write"; Podman's compat endpoint reports the
		// same operations lowercased ("read"/"write") plus extra op types
		// (rios/wios/dbytes/dios) that both engines already skip by omission.
		switch strings.ToLower(blk.Op) {
		case "read":
			read += int64(blk.Value)
		case "write":
			write += int64(blk.Value)
		}
	}
	c.IOBytesRead, c.IOBytesWrite = read, write
}
