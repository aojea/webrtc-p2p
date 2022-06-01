package p2p

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"sync"
	"time"

	"github.com/pion/turn/v2"
	"github.com/pion/webrtc/v3"
	"golang.org/x/net/proxy"
)

const (
	turnUser   = "magicturnUser;-)"
	turnSecret = "magicturnSecret;-)"
)

type signalMsg struct {
	Kind   string `json:"kind,omitempty"`   // message type
	Origin string `json:"origin,omitempty"` // sender id
	Target string `json:"target,omitempty"` // target id
	SDP    string `json:"sdp,omitempty"`    // SDP session description protocol
}

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
	msgCh := make(chan string)
	p.m[id] = msgCh
	return nil
}

// get a new user
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

type SignalServer struct {
	// config
	publicIP    string // public IP address needed for the TURN Server
	turnAddress string // address where the TURN server is listening internally

	// internal
	pool *pool
}

func NewSignalServer(publicIP string) *SignalServer {
	s := &SignalServer{
		publicIP: publicIP,
		pool:     &pool{m: map[string](chan string){}},
	}
	return s
}

func (s *SignalServer) Run(stopCh chan struct{}) error {
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer tcpListener.Close()
	s.turnAddress = tcpListener.Addr().String()
	t, err := turn.NewServer(turn.ServerConfig{
		Realm: "kcp",
		// Set AuthHandler callback
		// This is called everytime a user tries to authenticate with the TURN server
		// Return the key for that user, or false when no user is found
		AuthHandler: func(username string, realm string, srcAddr net.Addr) ([]byte, bool) {
			return turn.GenerateAuthKey(username, "kcp", turnSecret), true
		},
		// ListenerConfig is a list of Listeners and the configuration around them
		ListenerConfigs: []turn.ListenerConfig{
			{
				Listener: tcpListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
					RelayAddress: net.ParseIP(s.publicIP),
					Address:      "0.0.0.0",
				},
			},
		},
	})
	if err != nil {
		return err
	}
	log.Printf("TURN server listening on %s\n", s.turnAddress)
	defer t.Close()
	<-stopCh
	return nil
}

func (s *SignalServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Println("request received from ", r.RemoteAddr, r.URL, r.Header)
	proxyPath := path.Base(r.URL.Path)
	if proxyPath == "proxy" {
		s.turnProxyHandler(w, r)
		return
	}

	var msg signalMsg
	decoder := json.NewDecoder(r.Body)
	err := decoder.Decode(&msg)
	if err != nil {
		http.Error(w, "Bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	switch msg.Kind {
	case "register":
		s.registerHandler(w, r, msg.Origin)
	case "offer", "answer":
		s.exchangeHandler(w, r, msg)
	default:
		http.Error(w, "Unknown kind: "+msg.Kind, http.StatusBadRequest)
	}
}

func (s *SignalServer) exchangeHandler(w http.ResponseWriter, r *http.Request, msg signalMsg) {
	log.Println("connect request from ", msg.Origin, " to ", msg.Target)

	msgCh, err := s.pool.get(msg.Target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("sending offer to %s\n", msg.Target)
	data, err := json.Marshal(msg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	select {
	case msgCh <- string(data):
	case <-r.Context().Done():
		http.Error(w, r.Context().Err().Error(), http.StatusBadRequest)
	}
}

func (s *SignalServer) registerHandler(w http.ResponseWriter, r *http.Request, id string) {
	log.Println("connect request received for registering client", id)

	// register new client
	if err := s.pool.register(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer s.pool.unregister(id)

	msgCh, err := s.pool.get(id)
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
		case c = <-msgCh:
		case <-r.Context().Done():
			return
		}
		log.Println("sending msg to ", id)
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

func (s *SignalServer) turnProxyHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("connect request received for turn")

	destConn, err := net.DialTimeout("tcp", s.turnAddress, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer destConn.Close()

	log.Println("connect to turn server established")
	_, ok := w.(http.Hijacker)
	if !ok {
		panic("flusher not support")
	}
	clientConn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go func() {
		io.Copy(destConn, clientConn)
	}()
	io.Copy(clientConn, destConn)
}

var _ proxy.Dialer = (*turnDialer)(nil)

type turnDialer struct {
	address url.URL
	client  *http.Client
}

func (t *turnDialer) Dial(network string, addr string) (net.Conn, error) {
	log.Printf("dialing proxy %q ...", t.address.String())
	var d net.Dialer
	c, err := d.DialContext(context.TODO(), "tcp", t.address.Host)
	if err != nil {
		log.Println("dialing proxy: failed")
		return nil, fmt.Errorf("dialing proxy %v failed: %v", t.address, err)
	}
	log.Println("dialing proxy get /proxy 1111")

	fmt.Fprintf(c, "GET /proxy HTTP/1.1\r\nHost: %s\r\n\r\n", t.address.Host)
	log.Println("dialing proxy get /proxy 22222")
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, nil)
	log.Println("dialing proxy read response")
	if err != nil {
		log.Println("dialing proxy: error reading http response")
		return nil, fmt.Errorf("reading HTTP response from %s failed: %v", t.address.Host, err)
	}
	if res.StatusCode != 200 {
		log.Println("dialing proxy: error http code", res.StatusCode)
		return nil, fmt.Errorf(" HTTP response from %s status failed: %v", t.address.Host, res.Status)
	}

	// It's safe to discard the bufio.Reader here and return the
	// original TCP conn directly because we only use this for
	// TLS, and in TLS the client speaks first, so we know there's
	// no unbuffered data. But we can double-check.
	if br.Buffered() > 0 {
		return nil, fmt.Errorf("unexpected %d bytes of buffered data from CONNECT proxy %q",
			br.Buffered(), t.address.Host)
	}
	log.Printf("dialing proxy %q succeeded", t.address.String())
	return c, nil
}

func turnProxyDialer(proxyAddr url.URL, client *http.Client) proxy.Dialer {
	return &turnDialer{proxyAddr, client}
}

type SignalClient struct {
	// config
	id           string
	signalServer string
	client       *http.Client // TODO should we need to modify it?
	Handler      func(m signalMsg)
	// webrtc settings
	api *webrtc.API
	cfg webrtc.Configuration

	donec     chan struct{}
	closeOnce sync.Once
}

func NewSignalClient(id string, signalServer string) (*SignalClient, error) {
	u, err := url.Parse(signalServer)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()

	s := &SignalClient{
		id:           id,
		signalServer: signalServer,
		client:       &http.Client{Transport: transport},
		donec:        make(chan struct{}),
	}

	ws := webrtc.SettingEngine{}
	ws.DetachDataChannels()
	// Implementation specific, the signal server has embedded a TURN server
	turnDialer := turnProxyDialer(*u, s.client)
	ws.SetICEProxyDialer(turnDialer)
	s.api = webrtc.NewAPI(webrtc.WithSettingEngine(ws))
	// Fake entry since we use the proxied server embedded in the signaling server
	s.cfg = webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs:       []string{"turn:127.0.1.1:3478?transport=tcp"},
				Username:   turnUser,
				Credential: turnSecret,
			},
		},
	}
	return s, nil
}

func (s *SignalClient) Run(stopCh chan struct{}) error {
	registerMsg := signalMsg{
		Kind:   "register",
		Origin: s.id,
	}
	j, err := json.Marshal(registerMsg)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, s.signalServer, bytes.NewBuffer(j))
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	log.Println("client registered", s.id)

	// process messages received
	doneCh := make(chan struct{})
	deco := json.NewDecoder(resp.Body)
	go func() {
		for {
			var msg signalMsg
			err = deco.Decode(&msg)
			if err != nil {
				log.Printf("error decoding message: %v", err)
				select {
				case <-doneCh:
					return
				default:
					close(doneCh)
				}
			}
			s.handler(msg)
		}
	}()
	select {
	case <-stopCh:
	case <-doneCh:
	}
	return err
}

func (s *SignalClient) handler(m signalMsg) {
	if s.Handler == nil {
		return
	}
	s.Handler(m)
}

func (s *SignalClient) SendMessage(m signalMsg) error {
	j, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, s.signalServer, bytes.NewBuffer(j))
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return nil
}

type flushWrite struct {
	w io.Writer
	f http.Flusher
}

func (w *flushWrite) Write(data []byte) (int, error) {
	n, err := w.w.Write(data)
	w.f.Flush()
	return n, err
}

func (w *flushWrite) Close() error {
	// Currently server side close of connection is not supported in Go.
	// The server closes the connection when the http.Handler function returns.
	// We use connection context and cancel function as a work-around.
	return nil
}
