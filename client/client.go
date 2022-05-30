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

	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = d.Dial

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
