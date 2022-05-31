package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"golang.org/x/net/http2"

	"github.com/pion/p2p"
)

var (
	remote string // remote url
)

func main() {
	flag.StringVar(&remote, "remote", "http://localhost:9001", "signal server url")
	flag.Parse()

	fmt.Print("Press 'Enter' when both processes have started")
	if _, err := bufio.NewReader(os.Stdin).ReadBytes('\n'); err != nil {
		panic(err)
	}

	d, err := p2p.NewDialer("client_host", remote)
	if err != nil {
		panic(err)
	}

	tr := &http2.Transport{
		// So http2.Transport doesn't complain the URL scheme isn't 'https'
		AllowHTTP: true,
		// Pretend we are dialing a TLS endpoint. (Note, we ignore the passed tls.Config)
		DialTLS: func(network, addr string, cfg *tls.Config) (net.Conn, error) {
			return d.Dial(context.TODO(), network, addr)
		},
	}

	target := &url.URL{Host: "server_host", Scheme: "http"}

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
