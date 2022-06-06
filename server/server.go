package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pion/p2p"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

var (
	remote  string // remote url
	localID string
)

func main() {

	kubeconfig := flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&localID, "id", "", "identifier used for the webrtc exchange (default to hostname)")
	flag.StringVar(&remote, "remote", "", "signal server url")
	flag.Parse()

	// validation
	var config *rest.Config
	var err error
	if *kubeconfig != "" {
		// use the current context in kubeconfig
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			panic(err.Error())
		}
	} else {
		// creates the in-cluster config
		config, err = rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
	}

	if remote == "" {
		remote = os.Getenv("SIGNAL_SERVER_URL")
		if remote == "" {
			panic("url for signal server not set")
		}
	}
	_, err = url.Parse(remote)
	if err != nil {
		panic(err)
	}

	if localID == "" {
		localID, err = os.Hostname()
		if err != nil {
			panic(err)
		}
	}
	target, _, err := rest.DefaultServerURL(config.Host, "", schema.GroupVersion{}, true)
	if err != nil {
		panic(err)
	}
	// target.Path = pathPrefix
	// target.Path = "/"
	config.NextProtos = []string{"http/1.1"}
	transport, err := rest.TransportFor(config)
	if err != nil {
		panic(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Transport = transport
	proxy.Director = func(req *http.Request) {
		req.Host = target.Host
		originalDirector(req)
		klog.Infof("Forwarded request %s", req.URL)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		fmt.Println(resp)
		return nil

	}

	mux := http.NewServeMux()
	mux.Handle("/", proxy)
	mux.HandleFunc("/test", func(w http.ResponseWriter, req *http.Request) {
		fmt.Println("received request")
		time.Sleep(500 * time.Millisecond)
		w.Write([]byte(strings.Repeat("a", 1724)))

	})

	ln, err := p2p.NewListener(localID, remote)
	if err != nil {
		panic(err)
	}
	defer ln.Close()
	h2s := &http2.Server{}
	h1s := &http.Server{
		Handler: h2c.NewHandler(mux, h2s),
	}
	log.Fatal(h1s.Serve(ln))

}
