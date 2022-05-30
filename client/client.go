package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"github.com/pion/p2p"
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

	d, err := p2p.NewDialer("client_host", remote)
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
