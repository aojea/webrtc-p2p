package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pion/p2p"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/klog/v2"
)

var (
	remote string // remote url
)

func main() {
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}

	flag.StringVar(&remote, "remote", "http://localhost:9001", "signal server url")
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

	fmt.Print("Press 'Enter' when both processes have started")
	if _, err := bufio.NewReader(os.Stdin).ReadBytes('\n'); err != nil {
		panic(err)
	}

	ln, err := p2p.NewListener("server_host", remote)
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
