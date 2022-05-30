package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
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
	client := http.Client{Transport: tr}

	request, err := http.NewRequest("GET", "http://server_host/api/v1/namespaces/default/pods", nil)
	if err != nil {
		panic(err)
	}

	response, err := client.Do(request)
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()

	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		panic(err)
	}
	fmt.Println("BODY --------------->", string(body))
}
