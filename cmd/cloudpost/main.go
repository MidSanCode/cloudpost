// Command cloudpost is the single-binary mail center daemon.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"crypto/tls"

	"cloudpost/internal/db"
	"cloudpost/internal/fetch"
	"cloudpost/internal/filter"
	"cloudpost/internal/imapd"
	"cloudpost/internal/mailstore"
	"cloudpost/internal/pop3d"
	"cloudpost/internal/sender"
	"cloudpost/internal/smtpd"
	"cloudpost/internal/state"
	"cloudpost/internal/tlsutil"
	"cloudpost/internal/web"
)

var version = "1.0.0"

func main() {
	var dataDir string
	var webAddr string
	var smtpAddr string
	var pop3Addr string
	var imapAddr string
	var devReload bool
	flag.StringVar(&dataDir, "data", defaultDataDir(), "data directory")
	flag.StringVar(&webAddr, "web", "0.0.0.0:8080", "web UI listen address")
	flag.StringVar(&smtpAddr, "smtp", "", "SMTP listen address (default from config or 0.0.0.0:2525)")
	flag.StringVar(&pop3Addr, "pop3", "", "POP3 listen address (default from config or 0.0.0.0:1110)")
	flag.StringVar(&imapAddr, "imap", "", "IMAP listen address (default from config or 0.0.0.0:1143)")
	flag.BoolVar(&devReload, "dev", false, "serve webui from ./webui (development)")
	flag.Parse()

	log.SetFlags(log.LstdFlags)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}

	st := state.New(dataDir)
	st.Load()

	database, err := db.Open(dataDir)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer database.Close()

	store, err := mailstore.New(database, dataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	engine := &filter.Engine{Store: store}

	cfg := st.Config()
	domain := "localhost"
	if cfg != nil && cfg.PrimaryDomain != "" {
		domain = cfg.PrimaryDomain
	}

	// Outbound sender.
	snd := sender.New(sender.Deps{DB: database, State: st})
	go snd.Run(30 * time.Second)

	// Remote fetch worker.
	fetcher := fetch.New(fetch.Deps{Store: store, Engine: engine, DB: database})
	go fetcher.Run()

	// Inbound SMTP.
	smtpSrv := smtpd.New(smtpd.Deps{
		Store:  store,
		State:  st,
		Domain: domain,
		Hooks: smtpd.Hooks{
			EnqueueRaw: func(from string, rcpts []string, raw []byte) error {
				_, err := snd.Enqueue(from, rcpts, raw)
				return err
			},
			PostDeliver: func(accountID, msgID int64, raw []byte) {
				info := mailstore.ParseHeaders(raw)
				var to string
				for _, a := range info.To {
					to += a + ","
				}
				engine.Apply(accountID, msgID, 0, info.Subject, info.FromAddr, to, info.Snippet)
			},
		},
	})
	smtpListen := smtpAddr
	if smtpListen == "" {
		smtpListen = fmt.Sprintf("0.0.0.0:%d", portOf(cfg, "smtp", 2525))
	}
	if err := smtpSrv.Listen(smtpListen); err != nil {
		log.Printf("[smtp] listen %s failed: %v", smtpListen, err)
	} else {
		go func() {
			log.Printf("[smtp] listening on %s", smtpListen)
			if err := smtpSrv.Serve(); err != nil {
				log.Printf("[smtp] serve: %v", err)
			}
		}()
	}

	// POP3.
	pop3Srv := pop3d.New(pop3d.Deps{Store: store, State: st})
	pop3Listen := pop3Addr
	if pop3Listen == "" {
		pop3Listen = fmt.Sprintf("0.0.0.0:%d", portOf(cfg, "pop3", 1110))
	}
	if err := pop3Srv.Listen(pop3Listen); err != nil {
		log.Printf("[pop3] listen %s failed: %v", pop3Listen, err)
	} else {
		go func() {
			log.Printf("[pop3] listening on %s", pop3Listen)
			if err := pop3Srv.Serve(); err != nil {
				log.Printf("[pop3] serve: %v", err)
			}
		}()
	}

	// IMAP.
	imapSrv := imapd.New(imapd.Deps{Store: store, State: st, Domain: domain})
	imapListen := imapAddr
	if imapListen == "" {
		imapListen = fmt.Sprintf("0.0.0.0:%d", portOf(cfg, "imap", 1143))
	}
	if err := imapSrv.Listen(imapListen); err != nil {
		log.Printf("[imap] listen %s failed: %v", imapListen, err)
	} else {
		go func() {
			log.Printf("[imap] listening on %s", imapListen)
			if err := imapSrv.Serve(); err != nil {
				log.Printf("[imap] serve: %v", err)
			}
		}()
	}

	// Optional TLS for the web UI (CLOUDPOST_WEB_TLS=1): self-signed cert
	// generated on first run, reused afterwards.
	var webTLSCert *tls.Certificate
	if os.Getenv("CLOUDPOST_WEB_TLS") == "1" {
		if cert, err := tlsutil.EnsureCert(filepath.Join(dataDir, "certs"), domain); err == nil {
			webTLSCert = cert
			log.Printf("[web] TLS enabled (self-signed certificate for %s)", domain)
		} else {
			log.Printf("[web] TLS requested but certificate generation failed: %v", err)
		}
	}
	wrapTLS := func(ln net.Listener) net.Listener {
		if webTLSCert == nil || ln == nil {
			return ln
		}
		return tls.NewListener(ln, &tls.Config{
			Certificates: []tls.Certificate{*webTLSCert},
			MinVersion:   tls.VersionTLS12,
		})
	}
	scheme := "http"
	if webTLSCert != nil {
		scheme = "https"
	}

	// Web UI + API.
	pm := web.NewPortManager(smtpSrv, pop3Srv, imapSrv)
	pm.TrackProtocol(smtpListen, pop3Listen, imapListen)
	webSrv := web.New(web.Deps{
		State:     st,
		Store:     store,
		Engine:    engine,
		Sender:    snd,
		Fetcher:   fetcher,
		Version:   version,
		StartedAt: time.Now(),
		BlobDir:   filepath.Join(dataDir, "blobs"),
		StaticDir: webStaticDir(devReload),
		Ports:     pm,
	})
	webListen := webAddr
	// When -web was left at its default, honor the configured web port
	// (set by the installation wizard or the settings page).
	if webAddr == "0.0.0.0:8080" {
		if cfg != nil && cfg.WebPort > 0 {
			webListen = fmt.Sprintf("0.0.0.0:%d", cfg.WebPort)
		}
	}
	httpServer := &http.Server{
		Handler:           webSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("[web] cloudpost %s listening on %s://%s", version, scheme, webListen)
	log.Printf("[web] open the UI in your browser to run the installation wizard")

	// Serve the web UI; honor live port changes by swapping listeners.
	// On a port change the handler pre-binds the new listener and publishes
	// it via TakeWebSwap; closing the old listener wakes Serve() up.
	var webLn net.Listener
	for {
		if webLn == nil {
			ln, err := net.Listen("tcp", webListen)
			if err != nil {
				log.Fatalf("[web] %v", err)
			}
			webLn = wrapTLS(ln)
		}
		pm.TrackWebListener(webLn)
		err := httpServer.Serve(webLn)
		webLn = nil
		if swap := webSrv.TakeWebSwap(); swap != nil {
			webListen = swap.Addr().String()
			log.Printf("[web] serving on new port %s", webListen)
			webLn = wrapTLS(swap)
			continue
		}
		if err == http.ErrServerClosed || err == nil {
			return
		}
		log.Fatalf("[web] %v", err)
	}
}

func portOf(cfg *state.Config, key string, def int) int {
	if cfg == nil {
		return def
	}
	switch key {
	case "smtp":
		if cfg.SMTPPort > 0 {
			return cfg.SMTPPort
		}
	case "pop3":
		if cfg.POP3Port > 0 {
			return cfg.POP3Port
		}
	case "imap":
		if cfg.IMAPPort > 0 {
			return cfg.IMAPPort
		}
	case "web":
		if cfg.WebPort > 0 {
			return cfg.WebPort
		}
	}
	return def
}

func webStaticDir(dev bool) string {
	if dev {
		if st, err := os.Stat("webui"); err == nil && st.IsDir() {
			return "webui"
		}
	}
	exe, err := os.Executable()
	if err == nil {
		c := filepath.Join(filepath.Dir(exe), "webui")
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	if st, err := os.Stat("webui"); err == nil && st.IsDir() {
		return "webui"
	}
	return ""
}

func defaultDataDir() string {
	if v := os.Getenv("CLOUDPOST_DATA"); v != "" {
		return v
	}
	exe, err := os.Executable()
	if err == nil {
		return filepath.Join(filepath.Dir(exe), "cloudpost-data")
	}
	return "./cloudpost-data"
}
