package node

import (
	"config"
	"fmt"
	"net"
)

type MemberNode struct {
	// Address info formatted ip_address:port
	IP        string
	PID       int64
	Timestamp int64
	Alive     bool
}

// var introducer *net.UDPConn

func joinNetwork() {
	// dial the introducer, send a message
	conn, err := net.Dial("udp", config.Introducer)
	fmt.Fprintf(conn, "Hi UDP Server, How are you doing?")
	conn.close()
}

func Live(introducer bool) {
	for {
		if !introducer {
			joinNetwork()
		}
	}
}
