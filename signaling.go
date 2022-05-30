package p2p

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"

	"golang.org/x/net/proxy"
)

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

func turnProxyDialer(proxyAddr string) proxy.Dialer {
	return &httpconnect{proxyAddr}
}
