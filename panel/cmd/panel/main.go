package main

import (
	"context"
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
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cs2-panel/internal/auth"
	"cs2-panel/internal/db"
	"cs2-panel/internal/docker"
	"cs2-panel/internal/gametracker"
	"cs2-panel/internal/rcon"
	"cs2-panel/internal/web"
)

func main() {
	port := flag.Int("port", 8080, "HTTP listen port")
	password := flag.String("password", "", "Panel access password (required)")
	composeFile := flag.String("compose-file", "./docker-compose.yml", "Path to docker-compose.yml")
	defaultRCON := flag.String("rcon-default", "changeme", "Default RCON password for new servers")
	dbPath := flag.String("db", "tournament.db", "Path to SQLite database file")
	tlsEnabled := flag.Bool("tls", false, "Enable HTTPS with auto-generated self-signed certificate")
	tlsCert := flag.String("tls-cert", "", "Path to TLS certificate file (optional, auto-generated if not set)")
	tlsKey := flag.String("tls-key", "", "Path to TLS key file (optional, auto-generated if not set)")
	flag.Parse()

	if *password == "" {
		fmt.Fprintln(os.Stderr, "Error: --password is required")
		flag.Usage()
		os.Exit(1)
	}

	absCompose, err := filepath.Abs(*composeFile)
	if err != nil {
		log.Fatalf("resolve compose file: %v", err)
	}
	if _, err := os.Stat(absCompose); err != nil {
		log.Fatalf("compose file not found: %s", absCompose)
	}

	dc, err := docker.New()
	if err != nil {
		log.Fatalf("docker: %v", err)
	}

	rm := rcon.NewManager()
	defer rm.CloseAll()

	tm := gametracker.NewManager(
		func(ctx context.Context, name string) (<-chan string, func(), error) {
			return dc.StreamLogLines(ctx, name)
		},
		func(addr, password, command string) (string, error) {
			return rm.Execute(addr, password, command)
		},
	)
	defer tm.StopAll()

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close()

	a := auth.New(*password, database.DB)
	if *tlsEnabled {
		a.SetSecure(true)
	}

	h, err := web.NewHandler(dc, rm, tm, database, absCompose, *defaultRCON)
	if err != nil {
		log.Fatalf("handler: %v", err)
	}

	handler := web.SetupRoutes(a, h)

	addr := fmt.Sprintf("0.0.0.0:%d", *port)
	log.Printf("Compose file: %s", absCompose)

	if *tlsEnabled {
		certFile, keyFile := *tlsCert, *tlsKey
		if certFile == "" || keyFile == "" {
			certFile, keyFile, err = ensureSelfSignedCert()
			if err != nil {
				log.Fatalf("tls: %v", err)
			}
		}
		log.Printf("CS2 Panel listening on https://%s", addr)
		tlsCfg, err := tlsConfig(certFile, keyFile)
		if err != nil {
			log.Fatalf("tls config: %v", err)
		}
		srv := &http.Server{
			Addr:      addr,
			Handler:   handler,
			TLSConfig: tlsCfg,
			ErrorLog:  log.New(&tlsErrorFilter{}, "", 0),
		}
		if err := srv.ListenAndServeTLS("", ""); err != nil {
			log.Fatalf("server: %v", err)
		}
	} else {
		log.Printf("CS2 Panel listening on http://%s", addr)
		if err := http.ListenAndServe(addr, handler); err != nil {
			log.Fatalf("server: %v", err)
		}
	}
}

// ensureSelfSignedCert generates a self-signed TLS cert if one doesn't exist,
// or reuses the existing one. Stored in ~/.cs2-panel/
func ensureSelfSignedCert() (certPath, keyPath string, err error) {
	certPath = "cert.pem"
	keyPath = "key.pem"

	// Reuse existing cert if valid
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		log.Printf("Using existing self-signed cert: %s", certPath)
		return certPath, keyPath, nil
	}

	log.Printf("Generating self-signed TLS certificate...")

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate key: %w", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "CS2 Panel"},
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

	log.Printf("Self-signed cert saved to %s (valid 1 year)", certPath)
	log.Printf("Browsers will show a warning — click through to accept")
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

// tlsErrorFilter suppresses noisy TLS handshake errors from the server log.
type tlsErrorFilter struct{}

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
	return os.Stderr.Write(p)
}
