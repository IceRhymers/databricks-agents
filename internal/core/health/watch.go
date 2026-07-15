package health

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy"
)

// WatchProxy polls the proxy health endpoint and takes over the port if the
// owner process dies. Runs as a goroutine for non-owner sessions.
// logPrefix is used for log messages (e.g. "databricks-claude").
// onTakeover, when non-nil, is called once immediately after this process
// successfully binds the port and becomes the new primary proxy owner.
func WatchProxy(port int, handler http.Handler, tlsCert, tlsKey, logPrefix string, onTakeover func()) {
	scheme := "http"
	if tlsCert != "" && tlsKey != "" {
		scheme = "https"
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if ProxyHealthy(port, scheme) {
			continue
		}

		// Proxy is unreachable — try to bind the port and take over.
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue // another session grabbed it first
		}
		if _, err := proxy.Serve(ln, handler, tlsCert, tlsKey); err != nil {
			ln.Close()
			continue
		}
		log.Printf("%s: proxy owner died, took over on :%d", logPrefix, port)
		if onTakeover != nil {
			onTakeover()
		}
		return
	}
}
