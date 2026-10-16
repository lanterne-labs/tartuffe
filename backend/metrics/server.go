package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewServeMux returns a mux that serves Registry at /metrics. The web
// service and the daily pipeline both serve it, so Prometheus reads the
// counters of each process while it runs.
func NewServeMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{}))
	return mux
}

// ListenAndServe serves mux on port until the listener fails.
//
// It binds the IPv6 wildcard (dual-stack), not the loopback: the Prometheus
// of the observability service scrapes <service>.host.internal, and the
// private network of the host is IPv6-only (the *.host.internal names
// return AAAA records). The port has no public domain, so it stays private
// to the network of the host project.
func ListenAndServe(port string, mux http.Handler) error {
	return http.ListenAndServe("[::]:"+port, mux)
}
