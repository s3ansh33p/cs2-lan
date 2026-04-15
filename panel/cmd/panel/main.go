package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"unilan/internal/auth"
	"unilan/internal/db"
	"unilan/internal/docker"
	"unilan/internal/games/cs2/cstv"
	"unilan/internal/games/cs2/rcon"
	"unilan/internal/games/cs2/tracker"
	"unilan/internal/logger"
	"unilan/internal/web"

	// Register supported games. Adding a new game = add a blank import here.
	_ "unilan/internal/games/cs2"
)

func main() {
	port := flag.Int("port", 8080, "HTTP listen port")
	cstvPort := flag.Int("cstv-port", 8089, "Loopback-only HTTP port for the CSTV+ broadcast relay (game servers POST here)")
	password := flag.String("password", "", "Panel access password (required)")
	defaultRCON := flag.String("rcon-default", "changeme", "Default RCON password for new servers")
	dbPath := flag.String("db", "tournament.db", "Path to SQLite database file")
	tlsEnabled := flag.Bool("tls", false, "Enable HTTPS with auto-generated self-signed certificate")
	tlsCert := flag.String("tls-cert", "", "Path to TLS certificate file (optional, auto-generated if not set)")
	tlsKey := flag.String("tls-key", "", "Path to TLS key file (optional, auto-generated if not set)")
	logFile := flag.String("log-file", "panel.log", "Path to log file")
	cstvPublicDelay := flag.Duration("cstv-public-delay", 45*time.Second, "Minimum age before a CSTV fragment is served on the public URL. Makes the spectate button lag live by this much to block screen-peeking. 0 disables the gate.")
	flag.Parse()

	if *password == "" {
		fmt.Fprintln(os.Stderr, "Error: --password is required")
		flag.Usage()
		os.Exit(1)
	}

	// Initialize file logging — must happen before anything else
	lf, err := logger.Setup(*logFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: open log file: %v\n", err)
		os.Exit(1)
	}
	defer lf.Close()

	// Ensure demos directory exists for demo file storage
	os.MkdirAll("demos", 0755)

	// Compose file lives in the per-game folder. Path is relative to the
	// binary's working directory (typically `panel/`).
	composeFile := "../games/cs2/docker-compose.yml"
	absCompose, err := filepath.Abs(composeFile)
	if err != nil {
		slog.Error("resolve compose file", "err", err)
		os.Exit(1)
	}
	if _, err := os.Stat(absCompose); err != nil {
		slog.Error("compose file not found", "path", absCompose)
		os.Exit(1)
	}

	dc, err := docker.New()
	if err != nil {
		slog.Error("docker init failed", "err", err)
		os.Exit(1)
	}

	rm := rcon.NewManager()
	defer rm.CloseAll()

	// CSTV+ broadcast relay: CS2 servers POST live demo fragments to
	// http://127.0.0.1:<cstv-port>/cstv/<name>; the tracker's parser GETs
	// them back. The relay listens on a dedicated loopback-only HTTP port so
	// it keeps working when the main panel serves HTTPS on its own port.
	relay := cstv.NewRelay()
	// Cap fragment retention so a long match doesn't balloon panel memory.
	// The parser only needs the latest few fragments for its delta loop;
	// 128 fragments at ~3 s keyframe interval covers ~6 minutes, which is
	// enough headroom for retries and WS reconnects.
	relay.SetMaxFragments(128)
	relay.SetPublicDelay(*cstvPublicDelay)
	cstvMux := http.NewServeMux()
	cstvMux.Handle("/cstv/", http.StripPrefix("/cstv", relay.Handler()))
	cstvAddr := fmt.Sprintf("127.0.0.1:%d", *cstvPort)
	cstvSrv := &http.Server{Addr: cstvAddr, Handler: cstvMux}
	go func() {
		slog.Info("cstv relay listening", "addr", cstvAddr)
		if err := cstvSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("cstv relay exited", "err", err)
		}
	}()
	defer cstvSrv.Close()

	tm := tracker.NewManager(
		relay,
		func(addr, password, command string) (string, error) {
			return rm.Execute(addr, password, command)
		},
		*cstvPort,
	)
	defer tm.StopAll()

	database, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("database open failed", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	a := auth.New(*password, database.DB)
	if *tlsEnabled {
		a.SetSecure(true)
	}

	h, err := web.NewHandler(dc, rm, tm, relay, database, absCompose, *defaultRCON)
	if err != nil {
		slog.Error("handler init failed", "err", err)
		os.Exit(1)
	}

	mux := web.SetupRoutes(a, h)
	handler := web.LoggingMiddleware(mux)

	addr := fmt.Sprintf("0.0.0.0:%d", *port)

	slog.Info("startup",
		"port", *port,
		"tls", *tlsEnabled,
		"compose", absCompose,
		"db", *dbPath,
		"log_file", *logFile,
	)

	if *tlsEnabled {
		certFile, keyFile := *tlsCert, *tlsKey
		if certFile == "" || keyFile == "" {
			certFile, keyFile, err = ensureSelfSignedCert()
			if err != nil {
				slog.Error("tls cert generation failed", "err", err)
				os.Exit(1)
			}
		}
		slog.Info(fmt.Sprintf("listening on https://%s", addr))
		tlsCfg, err := tlsConfig(certFile, keyFile)
		if err != nil {
			slog.Error("tls config failed", "err", err)
			os.Exit(1)
		}
		srv := &http.Server{
			Addr:      addr,
			Handler:   handler,
			TLSConfig: tlsCfg,
			ErrorLog:  newTLSErrorLogger(lf),
		}
		if err := srv.ListenAndServeTLS("", ""); err != nil {
			slog.Error("server exited", "err", err)
			os.Exit(1)
		}
	} else {
		slog.Info(fmt.Sprintf("listening on http://%s", addr))
		if err := http.ListenAndServe(addr, handler); err != nil {
			slog.Error("server exited", "err", err)
			os.Exit(1)
		}
	}
}

// ensureSelfSignedCert generates a self-signed TLS cert if one doesn't exist,
// or reuses the existing one. Stored in the current working directory.
func ensureSelfSignedCert() (certPath, keyPath string, err error) {
	certPath = "cert.pem"
	keyPath = "key.pem"

	// Reuse existing cert if valid
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		slog.Info("reusing existing self-signed cert", "path", certPath)
		return certPath, keyPath, nil
	}

	slog.Info("generating self-signed TLS certificate")

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate key: %w", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "UniLAN Panel"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}

	// Add all local IPs as SANs so LAN clients can connect
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				template.IPAddresses = append(template.IPAddresses, ipnet.IP)
			}
		}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", "", fmt.Errorf("create cert: %w", err)
	}

	certOut, err := os.Create(certPath)
	if err != nil {
		return "", "", err
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		certOut.Close()
		return "", "", fmt.Errorf("encode cert: %w", err)
	}
	certOut.Close()

	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", "", err
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		keyOut.Close()
		return "", "", fmt.Errorf("encode key: %w", err)
	}
	keyOut.Close()

	slog.Info("self-signed cert saved", "path", certPath, "validity", "1 year")
	return certPath, keyPath, nil
}

func tlsConfig(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
	}, nil
}

// newTLSErrorLogger returns a log.Logger that suppresses noisy TLS handshake
// errors and writes everything else to the given writer.
func newTLSErrorLogger(w *os.File) *log.Logger {
	return log.New(&tlsErrorFilter{w: w}, "", 0)
}

// tlsErrorFilter suppresses noisy TLS handshake errors from the server log.
type tlsErrorFilter struct {
	w *os.File
}

func (f *tlsErrorFilter) Write(p []byte) (n int, err error) {
	msg := string(p)
	// Suppress common harmless TLS errors
	for _, substr := range []string{
		"TLS handshake error",
		"client sent an HTTP request to an HTTPS server",
		"tls: bad certificate",
		"EOF",
	} {
		if strings.Contains(msg, substr) {
			return len(p), nil
		}
	}
	return f.w.Write(p)
}
