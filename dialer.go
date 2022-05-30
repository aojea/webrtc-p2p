package p2p

import (
	"bufio"
	"context"
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

type Dialer struct {
	id           string
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
func NewDialer(id, remote string) (*Dialer, error) {
	u, err := url.Parse(remote)
	if err != nil {
		return nil, err
	}

	d := &Dialer{
		id:           id,
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
	turnDialer := turnProxyDialer(u.Host)
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
	res, err := http.PostForm(d.signalServer+"/register",
		url.Values{
			"id": {d.id},
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
	channelName := fmt.Sprintf("data_%s_%d", address, time.Now().Unix())
	dataChannel, err := peerConnection.CreateDataChannel(channelName, nil)
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
	})

	peerConnection.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		fmt.Printf("Peer Connection State has changed: %s\n", s.String())
	})

	// Register channel opening handling
	dataChannel.OnOpen(func() {
		fmt.Printf("Data channel '%s'-'%d' open.\n", dataChannel.Label(), dataChannel.ID())

		// Detach the data channel
		raw, dErr := dataChannel.Detach()
		if dErr != nil {
			panic(dErr)
		}
		incomingConn <- rwconn.NewConn(raw, raw, rwconn.SetWriteDelay(500*time.Millisecond))
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

	remoteID, _, _ := net.SplitHostPort(address)

	log.Printf("Send offer to id: %s\n", remoteID)
	_, err = http.PostForm(d.signalServer+"/offer",
		url.Values{
			"id":    {remoteID},
			"offer": {string(offerData)},
		})
	if err != nil {
		return nil, err
	}
	// TODO GET THE ANSWER
	// receive candidates
	go func() {
		br := bufio.NewReader(res.Body)
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
	}()
	select {
	case conn := <-incomingConn:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}

}
