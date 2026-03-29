package docker

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func (c *Client) Launch(ctx context.Context, req LaunchRequest, composeFile string) error {
	if !validName.MatchString(req.Name) {
		return fmt.Errorf("invalid server name %q: must be alphanumeric (hyphens/underscores allowed)", req.Name)
	}
	if req.Port < 1024 || req.Port > 65535 {
		return fmt.Errorf("port must be between 1024 and 65535")
	}

	// Check for existing container
	_, err := c.docker.ContainerInspect(ctx, "cs2-"+req.Name)
	if err == nil {
		return fmt.Errorf("server %q already exists", req.Name)
	}

	tvPort := req.Port + 5
	tvEnable := "0"
	if req.TV {
		tvEnable = "1"
	}

	args := []string{
		"compose", "-f", composeFile,
		"run", "-d",
		"--name", "cs2-" + req.Name,
		"-e", "CS2_SERVERNAME=" + req.Name,
		"-e", "CS2_PORT=" + strconv.Itoa(req.Port),
		"-e", "CS2_GAMEALIAS=" + req.Mode,
		"-e", "CS2_STARTMAP=" + req.Map,
		"-e", "CS2_MAXPLAYERS=" + strconv.Itoa(req.Players),
		"-e", "CS2_RCONPW=" + req.RCON,
		"-e", "CS2_PW=" + req.Password,
		"-e", "TV_ENABLE=" + tvEnable,
		"-e", "TV_PORT=" + strconv.Itoa(tvPort),
		"cs2",
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	// Set working directory to the compose file's directory for relative path resolution
	cmd.Dir = filepath.Dir(composeFile)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose run failed: %w\n%s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
