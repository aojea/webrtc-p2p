package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/component-base/cli"
	"k8s.io/kubectl/pkg/cmd"
	"k8s.io/kubectl/pkg/cmd/plugin"
	"k8s.io/kubectl/pkg/cmd/util"

	// Import to initialize client auth plugins.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"

	"github.com/pion/p2p"
)

func main() {

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

	defaultConfigFlags := genericclioptions.NewConfigFlags(true).WithDeprecatedPasswordFlag().WithDiscoveryBurst(300).WithDiscoveryQPS(50.0)
	defaultConfigFlags.WrapConfigFn = func(c *rest.Config) *rest.Config {
		c.Dial = d.Dial
		return c
	}
	command := cmd.NewDefaultKubectlCommandWithArgs(cmd.KubectlOptions{
		PluginHandler: cmd.NewDefaultPluginHandler(plugin.ValidPluginFilenamePrefixes),
		Arguments:     os.Args,
		ConfigFlags:   defaultConfigFlags,
		IOStreams:     genericclioptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr},
	})
	if err := cli.RunNoErrOutput(command); err != nil {
		time.Sleep(10 * time.Second)
		// Pretty-print the error and exit with an error.
		util.CheckErr(err)
	}

	time.Sleep(10 * time.Second)

}

type customRoundTripper struct {
	host string
	rt   http.RoundTripper
}

func (c *customRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	log.Println("RoundTrip", r.Host, c.host, r)
	r = r.Clone(context.TODO())
	r.Host = c.host
	return c.rt.RoundTrip(r)
}
