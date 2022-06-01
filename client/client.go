package main

import (
	"bufio"
	"flag"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"github.com/pion/p2p"
)

var (
	remote   string // remote url
	localID  string
	remoteID string
)

func main() {
	flag.StringVar(&remote, "remote", "", "signal server url")
	flag.StringVar(&localID, "id", "", "identifier used for the webrtc exchange (default to hostname)")
	flag.StringVar(&remoteID, "remote-id", "", "identifier target for the webrtc exchange")
	flag.Parse()

	if remote == "" {
		remote = os.Getenv("SIGNAL_SERVER_URL")
		if remote == "" {
			panic("url for signal server not set")
		}
	}
	_, err := url.Parse(remote)
	if err != nil {
		panic(err)
	}

	if localID == "" {
		localID, err = os.Hostname()
		if err != nil {
			panic(err)
		}
	}

	if remoteID == "" {
		panic("missing remote id for webrtc communication")
	}
	fmt.Print("Press 'Enter' when both processes have started")
	if _, err := bufio.NewReader(os.Stdin).ReadBytes('\n'); err != nil {
		panic(err)
	}

	d, err := p2p.NewDialer(localID, remote)
	if err != nil {
		panic(err)
	}

	/*
		tr := &http2.Transport{
			// So http2.Transport doesn't complain the URL scheme isn't 'https'
			AllowHTTP: true,
			// Pretend we are dialing a TLS endpoint. (Note, we ignore the passed tls.Config)
			DialTLS: func(network, addr string, cfg *tls.Config) (net.Conn, error) {
				return d.Dial(context.TODO(), network, addr)
			},
		}
	*/
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = d.Dial

	target := &url.URL{Host: remoteID, Scheme: "http"}

	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Transport = tr
	proxy.Director = func(req *http.Request) {
		req.Host = target.Host
		originalDirector(req)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		fmt.Println(resp)
		return nil

	}

	mux := http.NewServeMux()
	mux.Handle("/", proxy)
	http.ListenAndServe(":8080", mux)
}
