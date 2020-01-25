package node

import (
	"fmt"
	"log"
	"net"
	"os"

	"../config"
	"../hashing"
)

type MemberNode struct {
	// Address info formatted ip_address:port
	IP        string
	PID       int64
	Timestamp int64
	Alive     bool
}

var me MemberNode
var selfIP string
var selfPID int

const heartbeatPort = 5001

// var introducer *net.UDPConn

func joinNetwork() {
	// dial the introducer, send a message
	introducer, err := config.Introducer()
	if err != nil {
		log.Fatal("Introducer not configured.")
	}
	conn, err := net.Dial("udp", introducer)
	fmt.Fprintf(conn, "Hi UDP Server, How are you doing?")
	conn.Close()
}

func Live(introducer bool) {
	log.Printf("%s:%d", getSelfIP(), heartbeatPort)
	log.Println(selfIP)
	selfPID = hashing.GetPID(selfIP)
	log.Println(selfPID)

	for {
		if !introducer {
			joinNetwork()
		}
	}
}

func getSelfIP() string {
	var ip string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		log.Fatalf("Cannot get my IP")
		os.Exit(1)
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				ip = ipnet.IP.String()
			}
		}
	}
	return ip
}
