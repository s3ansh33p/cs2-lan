package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

type ServerInfo struct {
	Name         string
	ContainerID  string
	Status       string
	Port         int
	RCONPassword string
	GameMode     string
	Map          string
	MaxPlayers   int
	Password     string
	TVEnabled    bool
	TVPort       int
	IsTTY        bool
	ExtraCfg     string
}

type Client struct {
	docker *client.Client
}

func New() (*Client, error) {
	c, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Client{docker: c}, nil
}

func (c *Client) ListServers(ctx context.Context) ([]ServerInfo, error) {
	containers, err := c.docker.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("name", "cs2-")),
	})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	var (
		mu      sync.Mutex
		servers []ServerInfo
		wg      sync.WaitGroup
	)
	for _, ctr := range containers {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			info, err := c.InspectServer(ctx, id)
			if err != nil {
				return
			}
			mu.Lock()
			servers = append(servers, info)
			mu.Unlock()
		}(ctr.ID)
	}
	wg.Wait()
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return servers, nil
}

func (c *Client) InspectServer(ctx context.Context, nameOrID string) (ServerInfo, error) {
	ctr, err := c.docker.ContainerInspect(ctx, nameOrID)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("inspect container %s: %w", nameOrID, err)
	}

	env := parseEnvVars(ctr.Config.Env)
	name := strings.TrimPrefix(ctr.Name, "/")
	name = strings.TrimPrefix(name, "cs2-")

	port, _ := strconv.Atoi(env["CS2_PORT"])
	maxPlayers, _ := strconv.Atoi(env["CS2_MAXPLAYERS"])
	tvPort, _ := strconv.Atoi(env["TV_PORT"])
	tvEnabled := env["TV_ENABLE"] == "1"

	status := "unknown"
	if ctr.State != nil {
		status = ctr.State.Status
	}

	return ServerInfo{
		Name:         name,
		ContainerID:  ctr.ID[:12],
		Status:       status,
		Port:         port,
		RCONPassword: env["CS2_RCONPW"],
		GameMode:     env["CS2_GAMEALIAS"],
		Map:          env["CS2_STARTMAP"],
		MaxPlayers:   maxPlayers,
		Password:     env["CS2_PW"],
		TVEnabled:    tvEnabled,
		TVPort:       tvPort,
		IsTTY:        ctr.Config.Tty,
		ExtraCfg:     env["CS2_EXTRA_CFG"],
	}, nil
}

func (c *Client) StopServer(ctx context.Context, name string) error {
	containerName := "cs2-" + name
	timeout := 10
	err := c.docker.ContainerStop(ctx, containerName, container.StopOptions{Timeout: &timeout})
	if err != nil {
		return fmt.Errorf("stop %s: %w", containerName, err)
	}
	err = c.docker.ContainerRemove(ctx, containerName, container.RemoveOptions{})
	if err != nil {
		return fmt.Errorf("remove %s: %w", containerName, err)
	}
	return nil
}

// StreamLogs returns a log stream. It checks the container's TTY setting
// to determine if the stream needs demultiplexing.
func (c *Client) StreamLogs(ctx context.Context, name string) (io.ReadCloser, bool, error) {
	containerName := "cs2-" + name

	// Check if container uses TTY
	ctr, err := c.docker.ContainerInspect(ctx, containerName)
	if err != nil {
		return nil, false, fmt.Errorf("inspect %s: %w", containerName, err)
	}
	isTTY := ctr.Config.Tty
	log.Printf("StreamLogs %s: tty=%v", name, isTTY)

	reader, err := c.docker.ContainerLogs(ctx, containerName, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       "500",
	})
	if err != nil {
		return nil, false, fmt.Errorf("logs %s: %w", containerName, err)
	}

	return reader, isTTY, nil
}

// ReadLogLines reads from a Docker log stream and sends lines to the channel.
// If isTTY is false, the stream has 8-byte multiplex headers that must be stripped.
func ReadLogLines(reader io.Reader, isTTY bool, lines chan<- string, done <-chan struct{}) {
	if isTTY {
		// Raw stream, just scan lines
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			select {
			case <-done:
				return
			case lines <- scanner.Text():
			}
		}
	} else {
		// Multiplexed stream: 8-byte header + payload per frame
		// Header: [stream_type(1), 0, 0, 0, size(4 big-endian)]
		hdr := make([]byte, 8)
		for {
			_, err := io.ReadFull(reader, hdr)
			if err != nil {
				return
			}
			size := binary.BigEndian.Uint32(hdr[4:8])
			if size == 0 {
				continue
			}
			payload := make([]byte, size)
			_, err = io.ReadFull(reader, payload)
			if err != nil {
				return
			}
			// Split payload into lines (a single frame can contain multiple lines)
			scanner := bufio.NewScanner(bytes.NewReader(payload))
			for scanner.Scan() {
				text := scanner.Text()
				select {
				case <-done:
					return
				case lines <- text:
				}
			}
		}
	}
}

// StreamLogLines returns a channel of log lines for the gametracker.
// It handles demultiplexing internally. The caller must call cleanup when done.
func (c *Client) StreamLogLines(ctx context.Context, name string) (<-chan string, func(), error) {
	reader, isTTY, err := c.StreamLogs(ctx, name)
	if err != nil {
		return nil, nil, err
	}

	lines := make(chan string, 128)
	done := make(chan struct{})

	go func() {
		ReadLogLines(reader, isTTY, lines, done)
		close(lines)
	}()

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			close(done)
			reader.Close()
		})
	}

	return lines, cleanup, nil
}

// ContainerResourceStats holds CPU and memory usage for a container.
type ContainerResourceStats struct {
	CPUPercent float64
	MemUsageMB float64
	MemLimitMB float64
}

// GetContainerStats returns a one-shot CPU/memory snapshot for a container.
func (c *Client) GetContainerStats(ctx context.Context, containerID string) (ContainerResourceStats, error) {
	resp, err := c.docker.ContainerStats(ctx, containerID, false)
	if err != nil {
		return ContainerResourceStats{}, err
	}
	defer resp.Body.Close()

	var stats container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return ContainerResourceStats{}, err
	}

	// CPU%: (container CPU delta / system CPU delta) * num CPUs * 100
	var cpuPercent float64
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)
	if sysDelta > 0 && cpuDelta >= 0 {
		cpuPercent = (cpuDelta / sysDelta) * float64(stats.CPUStats.OnlineCPUs) * 100.0
	}

	memUsage := float64(stats.MemoryStats.Usage) / (1024 * 1024)
	memLimit := float64(stats.MemoryStats.Limit) / (1024 * 1024)

	return ContainerResourceStats{
		CPUPercent: cpuPercent,
		MemUsageMB: memUsage,
		MemLimitMB: memLimit,
	}, nil
}

func parseEnvVars(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}
