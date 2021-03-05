package kubernetes

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promauth"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promscrape/discoveryutils"
)

var apiServerTimeout = flag.Duration("promscrape.kubernetes.apiServerTimeout", 30*time.Minute, "How frequently to reload the full state from Kuberntes API server")

// apiConfig contains config for API server
type apiConfig struct {
	aw *apiWatcher
}

func (ac *apiConfig) mustStop() {
	ac.aw.mustStop()
}

var configMap = discoveryutils.NewConfigMap()

func getAPIConfig(sdc *SDConfig, baseDir string, swcFunc ScrapeWorkConstructorFunc) (*apiConfig, error) {
	v, err := configMap.Get(sdc, func() (interface{}, error) { return newAPIConfig(sdc, baseDir, swcFunc) })
	if err != nil {
		return nil, err
	}
	return v.(*apiConfig), nil
}

func newAPIConfig(sdc *SDConfig, baseDir string, swcFunc ScrapeWorkConstructorFunc) (*apiConfig, error) {
	ac, err := promauth.NewConfig(baseDir, sdc.BasicAuth, sdc.BearerToken, sdc.BearerTokenFile, sdc.TLSConfig)
	if err != nil {
		return nil, fmt.Errorf("cannot parse auth config: %w", err)
	}
	apiServer := sdc.APIServer
	if len(apiServer) == 0 {
		// Assume we run at k8s pod.
		// Discover apiServer and auth config according to k8s docs.
		// See https://kubernetes.io/docs/reference/access-authn-authz/service-accounts-admin/#service-account-admission-controller
		host := os.Getenv("KUBERNETES_SERVICE_HOST")
		port := os.Getenv("KUBERNETES_SERVICE_PORT")
		if len(host) == 0 {
			return nil, fmt.Errorf("cannot find KUBERNETES_SERVICE_HOST env var; it must be defined when running in k8s; " +
				"probably, `kubernetes_sd_config->api_server` is missing in Prometheus configs?")
		}
		if len(port) == 0 {
			return nil, fmt.Errorf("cannot find KUBERNETES_SERVICE_PORT env var; it must be defined when running in k8s; "+
				"KUBERNETES_SERVICE_HOST=%q", host)
		}
		apiServer = "https://" + net.JoinHostPort(host, port)
		tlsConfig := promauth.TLSConfig{
			CAFile: "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
		}
		acNew, err := promauth.NewConfig(".", nil, "", "/var/run/secrets/kubernetes.io/serviceaccount/token", &tlsConfig)
		if err != nil {
			return nil, fmt.Errorf("cannot initialize service account auth: %w; probably, `kubernetes_sd_config->api_server` is missing in Prometheus configs?", err)
		}
		ac = acNew
	}
	if !strings.Contains(apiServer, "://") {
		proto := "http"
		if sdc.TLSConfig != nil {
			proto = "https"
		}
		apiServer = proto + "://" + apiServer
	}
	for strings.HasSuffix(apiServer, "/") {
		apiServer = apiServer[:len(apiServer)-1]
	}
	var proxy func(*http.Request) (*url.URL, error)
	if proxyURL := sdc.ProxyURL.URL(); proxyURL != nil {
		proxy = http.ProxyURL(proxyURL)
	}
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:     ac.NewTLSConfig(),
			Proxy:               proxy,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     *apiServerTimeout,
		},
		Timeout: *apiServerTimeout,
	}
	aw := newAPIWatcher(client, apiServer, ac.Authorization, sdc.Namespaces.Names, sdc.Selectors, swcFunc)
	cfg := &apiConfig{
		aw: aw,
	}
	return cfg, nil
}
