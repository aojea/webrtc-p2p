package main

import (
	"bufio"
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

	ln, err := p2p.NewListener("server_host", remote)
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
