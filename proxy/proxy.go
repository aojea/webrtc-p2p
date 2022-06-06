package main

import (
	"flag"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	// Import to initialize client auth plugins.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

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

	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = d.Dial

	target := &url.URL{Host: remoteID, Scheme: "http"}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = tr
	mux := http.NewServeMux()
	mux.Handle("/", proxy)
	http.ListenAndServe("0.0.0.0:"+port, mux)
}
