package p2p

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/aojea/rwconn"
	"github.com/pion/webrtc/v3"
)

var _ net.Listener = (*Listener)(nil)

type Listener struct {
	id           string
	signalServer string // url to reach the signal server
	// webrtc settings
	api *webrtc.API
	cfg webrtc.Configuration

	connc     chan net.Conn
	donec     chan struct{}
	closeOnce sync.Once
}

func NewListener(id, remote string) (*Listener, error) {
	u, err := url.Parse(remote)
	if err != nil {
		return nil, err
	}

	ln := &Listener{
		id:           id,
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
	turnDialer := turnProxyDialer(u.Host)
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

	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("ICE Connection State has changed: %s\n", connectionState.String())
	})

	// Set ICE Candidate handler. As soon as a PeerConnection has gathered a candidate
	// send it to the other peer
	peerConnection.OnICECandidate(func(i *webrtc.ICECandidate) {
		// Send ICE Candidate via Websocket/HTTP/$X to remote peer
	})

	// Set the handler for Peer connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		fmt.Printf("Peer Connection State has changed: %s\n", s.String())
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

			ln.connc <- rwconn.NewConn(raw, raw, rwconn.SetWriteDelay(500*time.Millisecond))
		})
	})

	// register
	res, err := http.PostForm(ln.signalServer+"/register",
		url.Values{
			"id": {ln.id},
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
			panic(err)
		}

		log.Println("received offer")
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

		_, err = http.PostForm(ln.signalServer+"/offer",
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
