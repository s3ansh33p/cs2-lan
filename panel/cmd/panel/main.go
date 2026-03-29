package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"cs2-panel/internal/auth"
	"cs2-panel/internal/docker"
	"cs2-panel/internal/rcon"
	"cs2-panel/internal/web"
)

func main() {
	port := flag.Int("port", 8080, "HTTP listen port")
	password := flag.String("password", "", "Panel access password (required)")
	composeFile := flag.String("compose-file", "./docker-compose.yml", "Path to docker-compose.yml")
	defaultRCON := flag.String("rcon-default", "changeme", "Default RCON password for new servers")
	flag.Parse()

	if *password == "" {
		fmt.Fprintln(os.Stderr, "Error: --password is required")
		flag.Usage()
		os.Exit(1)
	}

	// Resolve compose file to absolute path
	absCompose, err := filepath.Abs(*composeFile)
	if err != nil {
		log.Fatalf("resolve compose file: %v", err)
	}
	if _, err := os.Stat(absCompose); err != nil {
		log.Fatalf("compose file not found: %s", absCompose)
	}

	// Initialize components
	dc, err := docker.New()
	if err != nil {
		log.Fatalf("docker: %v", err)
	}

	rm := rcon.NewManager()
	defer rm.CloseAll()

	a := auth.New(*password)

	h, err := web.NewHandler(dc, rm, absCompose, *defaultRCON)
	if err != nil {
		log.Fatalf("handler: %v", err)
	}

	handler := web.SetupRoutes(a, h)

	addr := fmt.Sprintf("0.0.0.0:%d", *port)
	log.Printf("CS2 Panel listening on http://%s", addr)
	log.Printf("Compose file: %s", absCompose)

	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server: %v", err)
	}
}
