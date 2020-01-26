package node

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"

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

var selfIP string
var selfPID int

const port = 6000

var introducerAddr net.UDPAddr
var heartbeatAddr net.UDPAddr

func Live(introducer bool, logf string) {
	selfIP = getSelfIP()
	selfPID = hashing.GetPID(selfIP)

	// So the program doesn't die
	var wg sync.WaitGroup
	wg.Add(1)

	// Initialize logging to file
	f, err := os.OpenFile(logf, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer f.Close()
	mw := io.MultiWriter(os.Stdout, f)
	log.SetOutput(mw)

	heartbeatAddr = net.UDPAddr{
		IP:   net.ParseIP(selfIP),
		Port: port,
	}

	// Join the network if you're not the introducer
	if !introducer {
		joinNetwork()
	}

	// Listen for messages
	go listen()

	wg.Wait()
}

// listen will eventually need to listen for everything, but for now:
// just need: network joins, heartbeats
func listen() {
	p := make([]byte, 2048)

	ser, err := net.ListenUDP("udp", &heartbeatAddr)
	if err != nil {
		log.Fatal(err)
	}

	// Begin the UDP listen loop
	for {
		_, remoteaddr, err := ser.ReadFromUDP(p)
		log.Printf("Read a message from %v %s \n", remoteaddr, p)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func joinNetwork() {
	// dial the introducer, send a message
	log.Printf("Joining via introducer at %s:%d", config.Introducer(), port)
	conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", config.Introducer(), port))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(conn, "Hi UDP Server, How are you doing?")
	conn.Close()
}

func getSelfIP() string {
	var ip string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		log.Fatalf("Cannot get self IP")
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
