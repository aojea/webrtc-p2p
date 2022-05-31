package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/pion/p2p"
)

// assume messages are id: string and offer: (json with the webrtc offer)
func main() {
	publicIP := flag.String("public-ip", "192.168.1.150", "IP Address that TURN can be contacted by.")
	port := flag.Int("port", 9001, "signal server http port")

	flag.Parse()

	if len(*publicIP) == 0 {
		log.Fatalf("'public-ip' is required")
	}
	s := p2p.NewSignalServer(*publicIP)
	closeCh := make(chan struct{})
	go s.Run(closeCh)

	mux := http.NewServeMux()
	mux.Handle("/", s)

	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), s); err != nil {
		panic(err)
	}

}
