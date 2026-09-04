package web

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
)

// ProtocolListener is the minimal surface a mail protocol server must expose
// so the port manager can rebind it at runtime. smtpd/pop3d/imapd all
// implement Listen/Serve/Close.
type ProtocolListener interface {
	Listen(addr string) error
	Serve() error
	Close()
}

// PortManager owns runtime port changes: it rebinds SMTP/POP3/IMAP listeners
// and swaps the web HTTP listener without restarting the process.
type PortManager struct {
	mu      sync.Mutex
	SMTP    ProtocolListener
	POP3    ProtocolListener
	IMAP    ProtocolListener
	current struct {
		web      net.Listener
		smtpAddr string
		pop3Addr string
		imapAddr string
	}
	prevWeb net.Listener // web listener replaced by BindNewWebListener, not yet closed
}

// NewPortManager wires the manager with the running servers.
func NewPortManager(smtpSrv, pop3Srv, imapSrv ProtocolListener) *PortManager {
	return &PortManager{SMTP: smtpSrv, POP3: pop3Srv, IMAP: imapSrv}
}

// TrackWebListener records the initial web listener.
func (pm *PortManager) TrackWebListener(ln net.Listener) {
	pm.mu.Lock()
	pm.current.web = ln
	pm.mu.Unlock()
}

// TrackProtocol records the initial protocol listen addresses.
func (pm *PortManager) TrackProtocol(smtpAddr, pop3Addr, imapAddr string) {
	pm.mu.Lock()
	pm.current.smtpAddr, pm.current.pop3Addr, pm.current.imapAddr = smtpAddr, pop3Addr, imapAddr
	pm.mu.Unlock()
}

// CurrentWebListener returns the live web listener (nil before tracking).
func (pm *PortManager) CurrentWebListener() net.Listener {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.current.web
}

// CurrentPorts returns the live ports for status display.
func (pm *PortManager) CurrentPorts() (webPort, smtpPort, pop3Port, imapPort int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	portOf := func(addr string) int {
		if _, p, err := net.SplitHostPort(addr); err == nil {
			v, _ := strconv.Atoi(p)
			return v
		}
		return 0
	}
	w := 0
	if pm.current.web != nil {
		w = portOf(pm.current.web.Addr().String())
	}
	return w, portOf(pm.current.smtpAddr), portOf(pm.current.pop3Addr), portOf(pm.current.imapAddr)
}

func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil && h != "" {
		return h
	}
	return "0.0.0.0"
}

// rebind stops a protocol server on its old address and starts it on the new
// one, keeping the old bind host. newAddr empty means no change.
func (pm *PortManager) rebind(name string, srv ProtocolListener, oldAddr, newAddr string) (string, error) {
	if newAddr == "" || newAddr == oldAddr {
		return oldAddr, nil
	}
	if srv == nil {
		return oldAddr, fmt.Errorf("%s server not wired", name)
	}
	// Pre-flight: bind the new port BEFORE closing the old listener; on
	// failure keep serving the old one untouched.
	probe, err := net.Listen("tcp", newAddr)
	if err != nil {
		return oldAddr, fmt.Errorf("%s: cannot bind %s: %v", name, newAddr, err)
	}
	_ = probe.Close()

	srv.Close()
	if err := srv.Listen(newAddr); err != nil {
		// The old port was freed; fall back to it so the service stays up.
		if ln2, fb := net.Listen("tcp", oldAddr); fb == nil {
			_ = ln2.Close()
			_ = srv.Listen(oldAddr)
			go func() {
				log.Printf("[ports] %s rebind to %s failed (%v); reverted to %s", name, newAddr, err, oldAddr)
				_ = srv.Serve()
			}()
			return oldAddr, fmt.Errorf("%s: rebind to %s failed, reverted to %s: %v", name, newAddr, oldAddr, err)
		}
		return oldAddr, fmt.Errorf("%s: rebind to %s failed: %v", name, newAddr, err)
	}
	go func() {
		log.Printf("[ports] %s rebound to %s", name, newAddr)
		if err := srv.Serve(); err != nil && !strings.Contains(err.Error(), "closed") {
			log.Printf("[ports] %s serve after rebind: %v", name, err)
		}
	}()
	return newAddr, nil
}

// ApplyProtocolPorts rebinds SMTP/POP3/IMAP whose port changed (0 = keep).
// The bind host of each service is preserved from its current listener.
func (pm *PortManager) ApplyProtocolPorts(smtpPort, pop3Port, imapPort int) (applied map[string]string, errs []string) {
	pm.mu.Lock()
	targets := []struct {
		name string
		srv  ProtocolListener
		old  string
		want int
		set  func(string)
	}{
		{"smtp", pm.SMTP, pm.current.smtpAddr, smtpPort, func(a string) { pm.current.smtpAddr = a }},
		{"pop3", pm.POP3, pm.current.pop3Addr, pop3Port, func(a string) { pm.current.pop3Addr = a }},
		{"imap", pm.IMAP, pm.current.imapAddr, imapPort, func(a string) { pm.current.imapAddr = a }},
	}
	pm.mu.Unlock()

	applied = map[string]string{}
	for _, t := range targets {
		if t.srv == nil || t.want <= 0 || t.old == "" {
			continue
		}
		wantAddr := net.JoinHostPort(hostOf(t.old), strconv.Itoa(t.want))
		newAddr, err := pm.rebind(t.name, t.srv, t.old, wantAddr)
		pm.mu.Lock()
		t.set(newAddr)
		pm.mu.Unlock()
		if err != nil {
			errs = append(errs, err.Error())
		} else if newAddr != t.old {
			applied[t.name] = newAddr
		}
	}
	return applied, errs
}

// BindNewWebListener binds a new web listener on the requested port (keeping
// the current bind host) and records it as the live one, WITHOUT closing the
// previous listener. The caller must publish the new listener to the serving
// loop first, then call ClosePreviousWebListener.
func (pm *PortManager) BindNewWebListener(port int) (net.Listener, error) {
	pm.mu.Lock()
	cur := pm.current.web
	pm.mu.Unlock()
	if cur == nil {
		return nil, fmt.Errorf("web listener not tracked")
	}
	if port == portOfAddr(cur.Addr().String()) {
		return cur, nil
	}
	addr := net.JoinHostPort(hostOf(cur.Addr().String()), strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("web: cannot bind %s: %v", addr, err)
	}
	pm.mu.Lock()
	pm.current.web = ln
	pm.prevWeb = cur
	pm.mu.Unlock()
	return ln, nil
}

// ClosePreviousWebListener closes the web listener that was replaced by the
// last BindNewWebListener call (no-op when nothing changed).
func (pm *PortManager) ClosePreviousWebListener() {
	pm.mu.Lock()
	prev := pm.prevWeb
	pm.prevWeb = nil
	pm.mu.Unlock()
	if prev != nil {
		_ = prev.Close()
		log.Printf("[ports] web listener switched; old listener closed")
	}
}

func portOfAddr(addr string) int {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		v, _ := strconv.Atoi(p)
		return v
	}
	return 0
}
