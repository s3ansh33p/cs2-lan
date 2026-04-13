package docker

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"unilan/internal/games"
)

type LaunchRequest struct {
	Name     string
	Port     int
	Mode     string
	Map      string
	Players  int
	Password string
	RCON     string
	TV       bool
	ExtraCfg string
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func (c *Client) Launch(ctx context.Context, req LaunchRequest, composeFile string) (LaunchRequest, error) {
	if !validName.MatchString(req.Name) {
		return req, fmt.Errorf("invalid server name %q: must be alphanumeric (hyphens/underscores allowed)", req.Name)
	}
	if req.Port < 1024 || req.Port > 65535 {
		return req, fmt.Errorf("port must be between 1024 and 65535")
	}

	// Check for existing container
	prefix := games.Default().ContainerPrefix()
	_, err := c.docker.ContainerInspect(ctx, prefix+req.Name)
	if err == nil {
		return req, fmt.Errorf("server %q already exists", req.Name)
	}

	// Resolve port conflicts against running servers
	servers, err := c.ListServers(ctx)
	if err != nil {
		return req, fmt.Errorf("failed to list servers for port check: %w", err)
	}
	usedPorts := make(map[int]bool)
	for _, s := range servers {
		usedPorts[s.Port] = true
		usedPorts[s.TVPort] = true
	}
	for usedPorts[req.Port] || usedPorts[req.Port+1000] {
		req.Port++
		if req.Port > 65535 {
			return req, fmt.Errorf("no available port found")
		}
	}

	tvPort := req.Port + 1000
	// TV (GOTV/CSTV) must be on: the live tracker consumes the CSTV+ broadcast.
	// The req.TV form flag is still honored downstream (it controls GOTV-specific
	// UI bits) but the engine-level TV must be on for event parsing.
	tvEnable := "1"

	args := []string{
		"compose", "-f", composeFile,
		"run", "-d",
		"--name", prefix + req.Name,
		"-e", "CS2_SERVERNAME=" + req.Name,
		"-e", "CS2_PORT=" + strconv.Itoa(req.Port),
		"-e", "CS2_GAMEALIAS=" + req.Mode,
		"-e", "CS2_STARTMAP=" + req.Map,
		"-e", "CS2_MAXPLAYERS=" + strconv.Itoa(req.Players),
		"-e", "CS2_RCONPW=" + req.RCON,
		"-e", "CS2_PW=" + req.Password,
		"-e", "TV_ENABLE=" + tvEnable,
		"-e", "TV_PORT=" + strconv.Itoa(tvPort),
		"-e", "CS2_LOG=on",
		"-e", "CS2_LOG_DETAIL=3",
		"-e", "CS2_LOG_ECHO=1",
	}

	if req.ExtraCfg != "" {
		args = append(args, "-e", "CS2_EXTRA_CFG="+req.ExtraCfg)
	}

	args = append(args, "cs2")

	cmd := exec.CommandContext(ctx, "docker", args...)
	// Set working directory to the compose file's directory for relative path resolution
	cmd.Dir = filepath.Dir(composeFile)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return req, fmt.Errorf("docker compose run failed: %w\n%s", err, strings.TrimSpace(string(output)))
	}
	return req, nil
}
