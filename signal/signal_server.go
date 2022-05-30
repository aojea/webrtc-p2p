package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/pion/turn/v2"
)

var (
	p           *pool
	turnAddress string
)

type pool struct {
	mu sync.Mutex
	m  map[string]chan string // user and channel associated
}

// register a new user
func (p *pool) register(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.m[id]; ok {
		return fmt.Errorf("user %s already registered", id)
	}
	candidateCh := make(chan string)
	p.m[id] = candidateCh
	return nil
}

// register a new user
func (p *pool) get(id string) (chan string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.m[id]; !ok {
		return nil, fmt.Errorf("user %s doesn't exist", id)
	}

	return p.m[id], nil
}

// unregister user
func (p *pool) unregister(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.m[id]; !ok {
		return
	}
	select {
	case <-p.m[id]:
	default:
		close(p.m[id])
	}

	delete(p.m, id)
}

func registerHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// register new client
	id := r.PostForm["id"][0]
	if err := p.register(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer p.unregister(id)

	candidateCh, err := p.get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("user %s registered\n", id)

	// OK - Flush headers and return control to the client to keep reading the body
	// First flush response headers
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// keep it open waiting for connection
	var c string
	for {

		select {
		case c = <-candidateCh:
		case <-r.Context().Done():
			return
		}
		fmt.Println("sending candidate to ", id)
		_, err := fmt.Fprintf(w, "%s\n", c)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func negotiateHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		panic(err)
	}
	id := r.PostForm["id"][0]

	candidateCh, err := p.get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("sending offer to %s\n", id)
	select {
	case candidateCh <- r.PostForm["offer"][0]:
	case <-r.Context().Done():
		http.Error(w, r.Context().Err().Error(), http.StatusBadRequest)
		return
	}
}

func turnProxy(w http.ResponseWriter, r *http.Request) {
	log.Println("connect request received")
	dest_conn, err := net.DialTimeout("tcp", turnAddress, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}
	client_conn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	}
	go transfer(dest_conn, client_conn)
	go transfer(client_conn, dest_conn)
}
func transfer(destination io.WriteCloser, source io.ReadCloser) {
	defer destination.Close()
	defer source.Close()
	io.Copy(destination, source)
}

// assume messages are id: string and offer: (json with the webrtc offer)
func main() {
	p = &pool{m: map[string](chan string){}}
	publicIP := flag.String("public-ip", "192.168.1.150", "IP Address that TURN can be contacted by.")
	port := flag.Int("port", 9001, "signal server http port")

	flag.Parse()

	if len(*publicIP) == 0 {
		log.Fatalf("'public-ip' is required")
	}
	// Create a TCP listener to pass into pion/turn
	// pion/turn itself doesn't allocate any TCP listeners, but lets the user pass them in
	// this allows us to add logging, storage or modify inbound/outbound traffic
	tcpListener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		log.Panicf("Failed to create TURN server listener: %s", err)
	}
	defer tcpListener.Close()

	// get port
	turnAddress = tcpListener.Addr().String()

	s, err := turn.NewServer(turn.ServerConfig{
		Realm: "kcp",
		// Set AuthHandler callback
		// This is called everytime a user tries to authenticate with the TURN server
		// Return the key for that user, or false when no user is found
		AuthHandler: func(username string, realm string, srcAddr net.Addr) ([]byte, bool) {
			return turn.GenerateAuthKey(username, "kcp", "pass"), true
		},
		// ListenerConfig is a list of Listeners and the configuration around them
		ListenerConfigs: []turn.ListenerConfig{
			{
				Listener: tcpListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
					RelayAddress: net.ParseIP(*publicIP),
					Address:      "0.0.0.0",
				},
			},
		},
	})
	if err != nil {
		log.Panic(err)
	}
	defer s.Close()

	mux := http.NewServeMux()

	mux.HandleFunc("/register", registerHandler)
	mux.HandleFunc("/offer", negotiateHandler)
	mux.HandleFunc("/turn", turnProxy)

	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), mux); err != nil {
		panic(err)
	}

}
