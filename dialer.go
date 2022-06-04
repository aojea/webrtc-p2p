package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/aojea/rwconn"
	"github.com/pion/webrtc/v3"
)

type Dialer struct {
	*SignalClient

	// store peer connections
	mu    sync.Mutex
	peers map[string]*webrtc.PeerConnection

	donec     chan struct{}
	closeOnce sync.Once
}

// NewDialer returns the side of the connection which will initiate
// new connections over the already established reverse connections.
func NewDialer(id, remote string) (*Dialer, error) {
	s, err := NewSignalClient(id, remote)
	if err != nil {
		return nil, err
	}
	d := &Dialer{
		donec: make(chan struct{}),
		peers: map[string]*webrtc.PeerConnection{},
	}

	d.SignalClient = s
	s.Handler = d.Handler

	go func() {
		for {
			log.Println("connecting to the signal server", remote)
			err := s.Run(d.donec)
			if err != nil {
				log.Printf("signaling client exited with error: %v\n", err)
			}
			// retry if donec was not closed
			select {
			case <-d.donec:
				return
			default:
				time.Sleep(1 * time.Second)
			}
		}
	}()

	return d, nil
}

func (d *Dialer) Handler(msg signalMsg) {
	switch msg.Kind {
	case "answer":
	default:
		log.Printf("Unexpected msg: %+v\n", msg)
		return
	}

	d.mu.Lock()
	peerConnection, ok := d.peers[msg.Origin]
	if !ok {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	answer := webrtc.SessionDescription{}
	err := json.Unmarshal([]byte(msg.SDP), &answer)
	if err != nil {
		panic(err)
	}
	// Set the remote SessionDescription
	err = peerConnection.SetRemoteDescription(answer)
	if err != nil {
		panic(err)
	}
}

func (d *Dialer) Dial(ctx context.Context, network string, address string) (net.Conn, error) {
	now := time.Now()
	defer log.Printf("dial to %s took %v", address, time.Since(now))

	log.Println("starting dialing to", address)
	target, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(10*time.Second))
	defer cancel()

	incomingConn := make(chan net.Conn)
	d.mu.Lock()
	peerConnection, ok := d.peers[target]
	if !ok {
		// Create a new RTCPeerConnection using the API object
		peerConnection, err = d.api.NewPeerConnection(d.cfg)
		if err != nil {
			d.mu.Unlock()
			return nil, err
		}

		// Set the handler for ICE connection state
		// This will notify you when the peer has connected/disconnected
		peerConnection.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
			log.Printf("ICE Connection State has changed: %s\n", s.String())
			if s == webrtc.ICEConnectionStateFailed {
				d.mu.Lock()
				peerConnection.Close()
				delete(d.peers, target)
				d.mu.Unlock()
			}
		})

		// Set ICE Candidate handler. As soon as a PeerConnection has gathered a candidate
		// send it to the other peer
		peerConnection.OnICECandidate(func(i *webrtc.ICECandidate) {
		})

		peerConnection.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
			log.Printf("Peer Connection State has changed: %s\n", s.String())
			if s == webrtc.PeerConnectionStateFailed ||
				s == webrtc.PeerConnectionStateDisconnected {
				d.mu.Lock()
				peerConnection.Close()
				delete(d.peers, target)
				d.mu.Unlock()
			}
		})

		d.peers[target] = peerConnection
	}
	d.mu.Unlock()
	if ok {
		// block until the connection is ready
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			if peerConnection.ConnectionState() == webrtc.PeerConnectionStateConnected {
				break
			}

		}
	}

	// Create a datachannel with label 'data'
	channelName := fmt.Sprintf("data_%s_%d", target, time.Now().Unix())
	dataChannel, err := peerConnection.CreateDataChannel(channelName, nil)
	if err != nil {
		return nil, err
	}

	// Register channel opening handling
	dataChannel.OnOpen(func() {
		log.Printf("Data channel '%s'-'%d' open.\n", dataChannel.Label(), dataChannel.ID())
		// Detach the data channel
		// raw contains the data channel open
		raw, dErr := dataChannel.Detach()
		if dErr != nil {
			panic(dErr)
		}
		c := &peerClose{peer: peerConnection}
		c.ReadWriteCloser = raw
		incomingConn <- rwconn.NewConn(c, c, rwconn.SetWriteDelay(500*time.Millisecond))
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

	offerData, err := json.Marshal(*peerConnection.LocalDescription())
	if err != nil {
		return nil, err
	}

	offerMsg := signalMsg{
		Kind:   "offer",
		Origin: d.id,
		Target: target,
		SDP:    string(offerData),
	}

	log.Println("dialing: sending offer to", target)
	err = d.SendMessage(offerMsg)
	if err != nil {
		panic(err)
	}

	select {
	case conn := <-incomingConn:
		log.Println("dialed complete to", address)
		return conn, nil
	case <-ctx.Done():
		log.Println("dialed timeout to", address)
		return nil, ctx.Err()
	}

}

type peerClose struct {
	io.ReadWriteCloser
	peer *webrtc.PeerConnection
	once sync.Once
}

func (p *peerClose) Close() error {
	p.once.Do(func() { p.peer.Close() })
	return nil
}
