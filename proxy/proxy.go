package main

import (
	"context"
	"crypto/tls"
	"flag"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	// Import to initialize client auth plugins.
	"golang.org/x/net/http2"

	"github.com/pion/p2p"
)

var (
	remoteID string
	port     string
)

func main() {
	flag.StringVar(&remoteID, "remote-id", "", "identifier target for the webrtc exchange")
	flag.StringVar(&port, "port", "8080", "listening proxy local port")
	flag.Parse()

	if remoteID == "" {
		panic("missing remote id for webrtc communication")
	}

	signalServer := os.Getenv("SIGNAL_SERVER_URL")
	if signalServer == "" {
		panic("url for signal server not set")
	}
	_, err := url.Parse(signalServer)
	if err != nil {
		panic(err)
	}

	localID, err := os.Hostname()
	if err != nil {
		panic(err)
	}

	d, err := p2p.NewDialer(localID, signalServer)
	if err != nil {
		panic(err)
	}

	//tr := http.DefaultTransport.(*http.Transport).Clone()
	//tr.DialContext = d.Dial

	tr := &http2.Transport{
		// So http2.Transport doesn't complain the URL scheme isn't 'https'
		AllowHTTP: true,
		// Pretend we are dialing a TLS endpoint. (Note, we ignore the passed tls.Config)
		DialTLS: func(network, addr string, cfg *tls.Config) (net.Conn, error) {
			return d.Dial(context.TODO(), network, addr)
		},
	}

	target := &url.URL{Host: remoteID, Scheme: "http"}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = tr
	mux := http.NewServeMux()
	mux.Handle("/", proxy)
	http.ListenAndServe("0.0.0.0:"+port, mux)
}
