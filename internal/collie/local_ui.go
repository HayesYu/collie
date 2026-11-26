package collie

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// defaultLocalUI is where a node serves the hub's web UI on its own computer.
const defaultLocalUI = "127.0.0.1:17788"

// nodeUI is this node's local UI address, told to the hub when connecting.
var nodeUI string

// startLocalUI makes the hub's web UI available at http://127.0.0.1:<port> on
// this computer, and returns that address ("" if it isn't serving). Browsers
// treat 127.0.0.1 as a secure origin, so Chrome offers "Install Collie" (a
// Dock app) and notifications work, which they don't on the hub's plain-HTTP
// LAN address. Each browser still signs in on its own; this only forwards.
func startLocalUI(ctx context.Context, addr, hub string) string {
	if addr == "" || addr == "off" {
		return ""
	}
	target, err := url.Parse(hub)
	if err != nil || target.Host == "" {
		log.Printf("本机界面：hub 地址不对（%q）", hub)
		return ""
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Header.Del("X-Forwarded-For")
		},
		FlushInterval: -1, // event streams (SSE) pass through as they come
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			msg := "连不上 hub（" + hub + "）：" + err.Error()
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": msg})
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(msg + "\n"))
		},
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil && addr == defaultLocalUI && (errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.EAFNOSUPPORT) || !hasIPv4Loopback()) {
		if ln6, err6 := net.Listen("tcp", "[::1]:17788"); err6 == nil {
			ln, err = ln6, nil
		}
	}
	if err != nil {
		log.Printf("本机界面没有启动（%s）：%v", addr, err)
		return ""
	}
	tcp := ln.Addr().(*net.TCPAddr)
	local := "http://" + localDialAddr(tcp, hasIPv4Loopback)
	log.Printf("本机界面：%s （转发到 %s；在 Chrome 里可以安装成 app）", local, hub)
	hs := &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		hs.Shutdown(sc)
	}()
	go func() {
		if err := hs.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("本机界面停止：%v", err)
		}
	}()
	return local
}

// localApp: if the browser asking runs on a computer with a local UI (a node
// that connects from the same address, or the hub's own machine), where it is.
// A page on the LAN address can't look at 127.0.0.1 itself.
func (s *Server) localApp(w http.ResponseWriter, r *http.Request) {
	ip := canonicalIP(splitHost(r.RemoteAddr))
	if ip == "" || isLoopbackHost(ip) {
		writeJSON(w, 200, map[string]any{})
		return
	}
	ms := s.herdr.Machines()
	for _, m := range ms {
		if !m.Local && m.Online && m.ui != "" && canonicalIP(m.ip) == ip {
			writeJSON(w, 200, map[string]any{"url": m.ui, "machine": m.Name})
			return
		}
	}
	if len(ms) > 0 {
		if localURL := s.localLoopbackURL(); localURL != "" {
			if addrs, err := net.InterfaceAddrs(); err == nil {
				for _, a := range addrs {
					var ifIP net.IP
					switch v := a.(type) {
					case *net.IPNet:
						ifIP = v.IP
					case *net.IPAddr:
						ifIP = v.IP
					}
					if ifIP != nil && canonicalIP(ifIP.String()) == ip {
						writeJSON(w, 200, map[string]any{"url": localURL, "machine": ms[0].Name})
						return
					}
				}
			}
		}
	}
	writeJSON(w, 200, map[string]any{})
}

func (s *Server) localLoopbackURL() string {
	if s.localAddr != "" {
		if !isLoopbackHost(s.localAddr) {
			return ""
		}
		return "http://" + s.localAddr
	}
	if s.port == 0 {
		return ""
	}
	host := "127.0.0.1"
	if !hasIPv4Loopback() {
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(s.port))
}
