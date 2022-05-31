package p2p

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/aojea/rwconn"
	"github.com/pion/webrtc/v3"
)

var _ net.Listener = (*Listener)(nil)

type Listener struct {
	*SignalClient

	// store peer connections
	mu    sync.Mutex
	peers map[string]*webrtc.PeerConnection

	connc     chan net.Conn
	donec     chan struct{}
	closeOnce sync.Once
}

func NewListener(id, remote string) (*Listener, error) {
	s, err := NewSignalClient(id, remote)
	if err != nil {
		return nil, err
	}

	ln := &Listener{
		peers: map[string]*webrtc.PeerConnection{},
		connc: make(chan net.Conn),
		donec: make(chan struct{}),
	}
	ln.SignalClient = s
	s.Handler = ln.Handler

	go func() {
		err := s.Run(ln.donec)
		if err != nil {
			log.Printf("signaling client exited with error: %v\n", err)
		}
	}()

	return ln, nil

}

func (ln *Listener) Handler(msg signalMsg) {
	var err error

	switch msg.Kind {
	case "offer":
	default:
		log.Printf("Unexpected msg: %+v\n", msg)
		return
	}

	ln.mu.Lock()
	peerConnection, ok := ln.peers[msg.Origin]
	if !ok {
		// Create a new RTCPeerConnection using the API object
		peerConnection, err = ln.api.NewPeerConnection(ln.cfg)
		if err != nil {
			ln.mu.Unlock()
			panic(err)
		}
		ln.peers[msg.Origin] = peerConnection
	}
	ln.mu.Unlock()

	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Printf("ICE Connection State has changed: %s\n", connectionState.String())
	})

	// Set ICE Candidate handler. As soon as a PeerConnection has gathered a candidate
	// send it to the other peer
	peerConnection.OnICECandidate(func(i *webrtc.ICECandidate) {
		// Send ICE Candidate via Websocket/HTTP/$X to remote peer
	})

	// Set the handler for Peer connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("Peer Connection State has changed: %s\n", s.String())
	})

	// Register data channel creation handling
	peerConnection.OnDataChannel(func(d *webrtc.DataChannel) {
		log.Printf("New DataChannel %s %d\n", d.Label(), d.ID())

		// Register channel opening handling
		d.OnOpen(func() {
			log.Printf("Data channel '%s'-'%d' open.\n", d.Label(), d.ID())
			// Detach the data channel
			raw, dErr := d.Detach()
			if dErr != nil {
				panic(dErr)
			}

			ln.connc <- rwconn.NewConn(raw, raw, rwconn.SetWriteDelay(500*time.Millisecond))
		})
	})

	log.Println("received offer")
	offer := webrtc.SessionDescription{}
	err = json.Unmarshal([]byte(msg.SDP), &offer)
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

	answerMsg := signalMsg{
		Kind:   "answer",
		Origin: ln.id,
		Target: msg.Origin,
		SDP:    string(answerData),
	}
	err = ln.SendMessage(answerMsg)
	if err != nil {
		panic(err)
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
