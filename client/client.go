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

func main() {
	flag.StringVar(&remote, "remote", "http://localhost:9001", "signal server url")
	flag.Parse()

	fmt.Print("Press 'Enter' when both processes have started")
	if _, err := bufio.NewReader(os.Stdin).ReadBytes('\n'); err != nil {
		panic(err)
	}

	d, err := NewDialer(remote)
	if err != nil {
		panic(err)
	}

	c, err := d.Dial(context.TODO(), "", "server_host")
	if err != nil {
		panic(err)
	}
	log.Println("Dialed connection")
	handleConn(c)
	// Block forever
	select {}
}

func handleConn(conn net.Conn) {
	go func() {
		for range time.NewTicker(5 * time.Second).C {
			message := "test message from client"
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

type Dialer struct {
	signalServer string // url to reach the signal server
	// webrtc settings
	api *webrtc.API
	cfg webrtc.Configuration
	//
	donec     chan struct{}
	closeOnce sync.Once
}

// NewDialer returns the side of the connection which will initiate
// new connections over the already established reverse connections.
func NewDialer(remote string) (*Dialer, error) {
	u, err := url.Parse(remote)
	if err != nil {
		return nil, err
	}

	d := &Dialer{
		signalServer: remote,
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
	d.api = webrtc.NewAPI(webrtc.WithSettingEngine(s))

	// Prepare the configuration
	d.cfg = webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs:       []string{"turn:127.0.1.1:3478?transport=tcp"},
				Username:   "user",
				Credential: "pass",
			},
		},
	}

	return d, nil
}

func (d *Dialer) Dial(ctx context.Context, network string, address string) (net.Conn, error) {
	now := time.Now()
	defer log.Printf("dial to %s took %v", address, time.Since(now))
	incomingConn := make(chan net.Conn)

	// register
	res, err := http.PostForm(remote+"/register",
		url.Values{
			"id": {"client_host"},
		})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("Unable to register on the signal server, status code %d", res.StatusCode)
	}

	// Create a new RTCPeerConnection using the API object
	peerConnection, err := d.api.NewPeerConnection(d.cfg)
	if err != nil {
		return nil, err
	}

	// Create a datachannel with label 'data'
	dataChannel, err := peerConnection.CreateDataChannel("data_"+address, nil)
	if err != nil {
		return nil, err
	}

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("ICE Connection State has changed: %s\n", connectionState.String())
	})

	// Set ICE Candidate handler. As soon as a PeerConnection has gathered a candidate
	// send it to the other peer
	peerConnection.OnICECandidate(func(i *webrtc.ICECandidate) {
		// Send ICE Candidate via Websocket/HTTP/$X to remote peer
	})

	// Register channel opening handling
	dataChannel.OnOpen(func() {
		fmt.Printf("Data channel '%s'-'%d' open.\n", dataChannel.Label(), dataChannel.ID())

		// Detach the data channel
		raw, dErr := dataChannel.Detach()
		if dErr != nil {
			panic(dErr)
		}

		incomingConn <- rwconn.NewConn(raw, raw)
	})

	// Create an offer to send to the browser
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		return nil, err
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	// Sets the LocalDescription, and starts our UDP listeners
	err = peerConnection.SetLocalDescription(offer)
	if err != nil {
		return nil, err
	}

	// Block until ICE Gathering is complete, disabling trickle ICE
	// we do this because we only can exchange one signaling message
	// in a production application you should exchange ICE Candidates via OnICECandidate
	<-gatherComplete

	// TODO SEND THE OFFER
	offerData, err := json.Marshal(*peerConnection.LocalDescription())
	if err != nil {
		return nil, err
	}

	_, err = http.PostForm(remote+"/offer",
		url.Values{
			"id":    {address},
			"offer": {string(offerData)},
		})
	if err != nil {
		return nil, err
	}
	// TODO GET THE ANSWER
	// receive candidates
	go func() {
		br := bufio.NewReader(res.Body)
		for {
			line, err := br.ReadSlice('\n')
			if err != nil {
				return
			}
			answer := webrtc.SessionDescription{}
			err = json.Unmarshal(line, &answer)
			if err != nil {
				panic(err)
			}
			// Set the remote SessionDescription
			err = peerConnection.SetRemoteDescription(answer)
			if err != nil {
				panic(err)
			}
		}
	}()

	select {
	case conn := <-incomingConn:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}

}

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
