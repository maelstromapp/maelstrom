package cert

import (
	"crypto/tls"
	"fmt"
	log "github.com/mgutz/logxi/v1"
	"github.com/mholt/certmagic"
	"net"
	"net/http"
	"time"
)

type CertMagicOptions struct {
	Email string
}

func NewCertMagicWrapper(options CertMagicOptions) *CertMagicWrapper {
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(cert certmagic.Certificate) (certmagic.Config, error) {
			return certmagic.Config{}, nil
		},
	})

	magic := certmagic.New(cache, certmagic.Config{
		Email:  options.Email,
		Agreed: true,
	})

	hostCh := make(chan string)
	go manageHosts(hostCh, magic)

	wrapper := &CertMagicWrapper{
		magic:  magic,
		hosts:  make(map[string]bool),
		hostCh: hostCh,
	}
	wrapper.initOnDemand()
	return wrapper
}

type CertMagicWrapper struct {
	magic  *certmagic.Config
	hosts  map[string]bool
	hostCh chan string
}

func (c *CertMagicWrapper) initOnDemand() {
	onDemand := c.magic.OnDemand
	if onDemand == nil {
		c.magic.OnDemand = &certmagic.OnDemandConfig{
			DecisionFunc: func(name string) error {
				c.AddHost(name)
				return nil
			},
		}
	} else {
		decisionFunc := onDemand.DecisionFunc
		onDemand.DecisionFunc = func(name string) error {
			c.AddHost(name)
			return decisionFunc(name)
		}
	}
}

func (c *CertMagicWrapper) Start(mux http.Handler, httpPort int, httpsPort int) ([]*http.Server, error) {
	httpListener, err := net.Listen("tcp", fmt.Sprintf(":%d", httpPort))
	if err != nil {
		return nil, fmt.Errorf("letsencrypt: unable to start HTTP listener: %v", err)
	}
	httpsListener, err := tls.Listen("tcp", fmt.Sprintf(":%d", httpsPort), c.magic.TLSConfig())
	if err != nil {
		return nil, fmt.Errorf("letsencrypt: unable to start HTTPS listener: %v", err)
	}

	httpServer := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       5 * time.Second,
		Handler:           c.magic.HTTPChallengeHandler(http.HandlerFunc(c.httpRedirectHandler)),
	}
	httpsServer := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       5 * time.Minute,
		Handler:           mux,
	}

	go logErr("httpServer.Serve", httpServer.Serve(httpListener))
	go logErr("httpsServer.Serve", httpsServer.Serve(httpsListener))

	return []*http.Server{httpServer, httpsServer}, nil
}

func (c *CertMagicWrapper) AddHost(host string) {
	go func() {
		c.hostCh <- host
	}()
}

// adapted from Matt Holt's certmagic:
// https://github.com/mholt/certmagic/blob/master/certmagic.go#L141
func (c *CertMagicWrapper) httpRedirectHandler(w http.ResponseWriter, r *http.Request) {
	toURL := "https://"

	// since we redirect to the standard HTTPS port, we
	// do not need to include it in the redirect URL
	requestHost, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		requestHost = r.Host // host probably did not contain a port
	}

	// make sure certmagic knows about this domain
	c.AddHost(requestHost)

	toURL += requestHost
	toURL += r.URL.RequestURI()

	// get rid of this disgusting unencrypted HTTP connection
	w.Header().Set("Connection", "close")

	http.Redirect(w, r, toURL, http.StatusMovedPermanently)
}

func manageHosts(hostCh <-chan string, magic *certmagic.Config) {
	hosts := make(map[string]bool, 0)

	for h := range hostCh {
		_, ok := hosts[h]
		if !ok {
			hosts[h] = true
			hostSlice := make([]string, len(hosts))
			i := 0
			for h, _ := range hosts {
				hostSlice[i] = h
				i++
			}
			err := magic.Manage(hostSlice)
			if err != nil {
				log.Error("cert: error in magic.Manage", "err", err, "hosts", hostSlice)
			}
		}
	}
}

func logErr(msg string, err error) {
	if err != nil {
		log.Error(msg, "err", err)
	}
}
