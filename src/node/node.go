package node

import (
	"fmt"
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

const introducerPort = 6002
const heartbeatPort = 6001

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

	log.SetOutput(f)

	if !introducer {
		introducerAddr = net.UDPAddr{
			IP:   net.ParseIP(config.Introducer()),
			Port: introducerPort,
		}
		joinNetwork()
	} else {
		introducerAddr = net.UDPAddr{
			IP:   net.ParseIP(selfIP),
			Port: introducerPort,
		}
		go listenForIntroductions()
	}

	// TODO: does this really need to be so large?
	heartbeatAddr = net.UDPAddr{
		IP:   net.ParseIP(selfIP),
		Port: heartbeatPort,
	}

	wg.Wait()
	// go listenForHeartbeat()
}

// todo move listen loops to own package
func listenForIntroductions() {
	p := make([]byte, 2048)

	ser, err := net.ListenUDP("udp", &introducerAddr)
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

func listenForHeartbeat() {
	p := make([]byte, 2048)

	ser, err := net.ListenUDP("udp", &heartbeatAddr)
	if err != nil {
		log.Fatal(err)
	}

	// Begin the UDP listen loop
	for {
		_, remoteaddr, err := ser.ReadFromUDP(p)
		log.Println("Read a message from %v %s \n", remoteaddr, p)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func joinNetwork() {
	// dial the introducer, send a message
	conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", introducerAddr.IP, introducerAddr.Port))
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
