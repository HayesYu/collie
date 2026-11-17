package collie

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// defaultLocalUI is where a node serves the hub's web UI on its own computer.
const defaultLocalUI = "127.0.0.1:17788"

// serveLocalUI makes the hub's web UI available at http://127.0.0.1:<port> on
// this computer. Browsers treat 127.0.0.1 as a secure origin, so Chrome offers
// "Install Collie" (a Dock app) and notifications work, which they don't on
// the hub's plain-HTTP LAN address. Each browser still signs in with its own
// pairing code; this only forwards requests.
func serveLocalUI(ctx context.Context, addr, hub string) {
	if addr == "" || addr == "off" {
		return
	}
	target, err := url.Parse(hub)
	if err != nil || target.Host == "" {
		log.Printf("本机界面：hub 地址不对（%q）", hub)
		return
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
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("连不上 hub（" + hub + "）：" + err.Error() + "\n"))
		},
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("本机界面没有启动（%s）：%v", addr, err)
		return
	}
	host := addr
	if strings.HasPrefix(host, "0.0.0.0:") {
		host = "127.0.0.1:" + strings.TrimPrefix(host, "0.0.0.0:")
	}
	log.Printf("本机界面：http://%s （转发到 %s；在 Chrome 里可以安装成 app）", host, hub)
	hs := &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		hs.Shutdown(sc)
	}()
	if err := hs.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Printf("本机界面停止：%v", err)
	}
}
