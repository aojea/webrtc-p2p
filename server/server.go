package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/aojea/rwconn"
	"github.com/pion/webrtc/v3"

	"golang.org/x/net/proxy"
)

const messageSize = 1024

var (
	remote string // remote url
)

var _ net.Listener = (*Listener)(nil)

type Listener struct {
	signalServer string // url to reach the signal server
	// webrtc settings
	api *webrtc.API
	cfg webrtc.Configuration

	connc     chan net.Conn
	donec     chan struct{}
	closeOnce sync.Once
}

func NewListener(remote string) (*Listener, error) {
	u, err := url.Parse(remote)
	if err != nil {
		return nil, err
	}

	ln := &Listener{
		signalServer: remote,
		connc:        make(chan net.Conn),
		donec:        make(chan struct{}),
	}

	// Since this behavior diverges from the WebRTC API it has to be
	// enabled using a settings engine. Mixing both detached and the
	// OnMessage DataChannel API is not supported.
	// Create a SettingEngine and enable Detach
	s := webrtc.SettingEngine{}
	s.DetachDataChannels()

	// Implementation specific, the signal server has embedded a TURN server
	turnDialer := turnProxy(u.Host)
	s.SetICEProxyDialer(turnDialer)

	// Create an API object with the engine
	ln.api = webrtc.NewAPI(webrtc.WithSettingEngine(s))

	// Prepare the configuration
	ln.cfg = webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs:       []string{"turn:127.0.1.1:3478?transport=tcp"},
				Username:   "user",
				Credential: "pass",
			},
		},
	}

	go ln.run()

	return ln, nil

}

func (ln *Listener) run() {

	// Create a new RTCPeerConnection using the API object
	peerConnection, err := ln.api.NewPeerConnection(ln.cfg)
	if err != nil {
		panic(err)
	}

	// Set the handler for Peer connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		fmt.Printf("Peer Connection State has changed: %s\n", s.String())

		if s == webrtc.PeerConnectionStateFailed {
			// Wait until PeerConnection has had no network activity for 30 seconds or another failure. It may be reconnected using an ICE Restart.
			// Use webrtc.PeerConnectionStateDisconnected if you are interested in detecting faster timeout.
			// Note that the PeerConnection may come back from PeerConnectionStateDisconnected.
			fmt.Println("Peer Connection has gone to failed exiting")
		}
	})

	// Set ICE Candidate handler. As soon as a PeerConnection has gathered a candidate
	// send it to the other peer
	peerConnection.OnICECandidate(func(i *webrtc.ICECandidate) {
		// Send ICE Candidate via Websocket/HTTP/$X to remote peer
	})

	// Register data channel creation handling
	peerConnection.OnDataChannel(func(d *webrtc.DataChannel) {
		fmt.Printf("New DataChannel %s %d\n", d.Label(), d.ID())

		// Register channel opening handling
		d.OnOpen(func() {
			fmt.Printf("Data channel '%s'-'%d' open.\n", d.Label(), d.ID())

			// Detach the data channel
			raw, dErr := d.Detach()
			if dErr != nil {
				panic(dErr)
			}

			ln.connc <- rwconn.NewConn(raw, raw)
		})
	})

	// register
	res, err := http.PostForm(remote+"/register",
		url.Values{
			"id": {"server_host"},
		})

	if err != nil {
		panic(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		panic(fmt.Errorf("status code %d", res.StatusCode))
	}

	br := bufio.NewReader(res.Body)
	for {
		line, err := br.ReadSlice('\n')
		if err != nil {
			return
		}

		offer := webrtc.SessionDescription{}
		err = json.Unmarshal(line, &offer)
		if err != nil {
			panic(err)
		}

		// Set the remote SessionDescription
		err = peerConnection.SetRemoteDescription(offer)
		if err != nil {
			panic(err)
		}

		// Create answer
		answer, err := peerConnection.CreateAnswer(nil)
		if err != nil {
			panic(err)
		}

		// Create channel that is blocked until ICE Gathering is complete
		gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
		// Sets the LocalDescription, and starts our UDP listeners
		err = peerConnection.SetLocalDescription(answer)
		if err != nil {
			panic(err)
		}

		// Block until ICE Gathering is complete, disabling trickle ICE
		// we do this because we only can exchange one signaling message
		// in a production application you should exchange ICE Candidates via OnICECandidate
		<-gatherComplete

		// Output the answer in base64 so we can paste it in browser
		answerData, err := json.Marshal(*peerConnection.LocalDescription())
		if err != nil {
			panic(err)
		}

		_, err = http.PostForm(remote+"/offer",
			url.Values{
				"id":    {"client_host"},
				"offer": {string(answerData)},
			})
		if err != nil {
			panic(err)
		}

	}

}

// Accept blocks and returns a new connection, or an error.
func (ln *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-ln.connc:
		return c, nil
	case <-ln.donec:
		return nil, fmt.Errorf("Listener closed")
	}
}

// Close closes the Listener, making future Accept calls return an
// error.
func (ln *Listener) Close() error {
	ln.closeOnce.Do(ln.close)
	return nil
}

func (ln *Listener) close() {
	close(ln.connc)
	close(ln.donec)
}

// Addr returns a dummy address. This exists only to conform to the
// net.Listener interface.
func (ln *Listener) Addr() net.Addr { return connAddr{} }

type connAddr struct{}

func (connAddr) Network() string { return "conn" }
func (connAddr) String() string  { return "conn" }

type httpconnect struct {
	proxyAddr string
}

// ref https://github.com/golang/build/blob/e12c9d226b16d4d335b515404895f626b6beee14/cmd/buildlet/reverse.go#L197
func (h *httpconnect) Dial(network string, addr string) (net.Conn, error) {
	log.Printf("dialing proxy %q ...", h.proxyAddr)
	var d net.Dialer
	c, err := d.DialContext(context.TODO(), "tcp", h.proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("dialing proxy %q failed: %v", h.proxyAddr, err)
	}
	fmt.Fprintf(c, "GET /turn HTTP/1.1\r\nHost: %s\r\n\r\n", h.proxyAddr)
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		return nil, fmt.Errorf("reading HTTP response from CONNECT to %s via proxy %s failed: %v",
			addr, h.proxyAddr, err)
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("proxy error from %s while dialing %s: %v", h.proxyAddr, addr, res.Status)
	}

	// It's safe to discard the bufio.Reader here and return the
	// original TCP conn directly because we only use this for
	// TLS, and in TLS the client speaks first, so we know there's
	// no unbuffered data. But we can double-check.
	if br.Buffered() > 0 {
		return nil, fmt.Errorf("unexpected %d bytes of buffered data from CONNECT proxy %q",
			br.Buffered(), h.proxyAddr)
	}
	return c, nil
}
func turnProxy(proxyAddr string) proxy.Dialer {
	return &httpconnect{proxyAddr}
}

func main() {

	flag.StringVar(&remote, "remote", "http://localhost:9001", "signal server url")
	flag.Parse()

	fmt.Print("Press 'Enter' when both processes have started")
	if _, err := bufio.NewReader(os.Stdin).ReadBytes('\n'); err != nil {
		panic(err)
	}

	ln, err := NewListener(remote)
	if err != nil {
		panic(err)
	}
	defer ln.Close()
	for {
		// Listen for an incoming connection.
		conn, err := ln.Accept()
		if err != nil {
			fmt.Println("Error accepting: ", err.Error())
			os.Exit(1)
		}
		log.Println("Received connection")
		// Handle connections in a new goroutine.
		go handleConn(conn)
	}

}

func handleConn(conn net.Conn) {
	go func() {
		for range time.NewTicker(5 * time.Second).C {
			message := "test message from server"
			_, err := conn.Write([]byte(message))
			if err != nil {
				panic(err)
			}
		}
	}()

	for {
		buffer := make([]byte, messageSize)
		n, err := conn.Read(buffer)
		if err != nil {
			fmt.Println("Datachannel closed; Exit the readloop:", err)
			continue
		}
		fmt.Printf("Server Message from DataChannel: %s\n", string(buffer[:n]))

	}
}
