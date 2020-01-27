package node

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"../config"
	"../hashing"
	"../spec"
)

var selfIP string
var selfPID int

var memberMap = make(map[int]*spec.MemberNode)

const m int = 7
const introducerPort = 6001
const port = 6000

var heartbeatAddr net.UDPAddr

func Live(introducer bool, logf string) {
	selfIP = getSelfIP()
	selfPID = hashing.GetPID(selfIP, m)
	spec.ReportOnline(selfIP, selfPID, introducer)

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
	} else {
		go listenForJoins()
	}

	// Listen for messages
	go listen()

	wg.Wait()
}

func listenForJoins() {
	p := make([]byte, 2048)
	ser, err := net.ListenUDP("udp", &net.UDPAddr{
		IP:   net.ParseIP(selfIP),
		Port: introducerPort,
	})
	if err != nil {
		log.Fatal(err)
	}

	// Begin the UDP listen loop
	for {
		_, remoteaddr, err := ser.ReadFromUDP(p)
		if err != nil {
			log.Fatal(err)
		}

		// Check if message code == JOIN
		var bb [][]byte = bytes.Split(p, []byte(","))
		replyCode, err := strconv.Atoi(string(bb[0][0]))
		if err != nil {
			log.Fatal(err)
		}

		switch replyCode {
		case spec.JOIN:
			// Update our own member map
			newPID := hashing.GetPID(remoteaddr.IP.String(), m)
			memberMap[newPID] = &spec.MemberNode{
				IP:        remoteaddr.IP.String(),
				Timestamp: time.Now().Unix(),
				Alive:     true,
			}
			log.Printf("[JOIN] (IP=%s) (PID=%d) joined network", remoteaddr.IP.String(), newPID)

			// Send the joiner a membership map so that it can discover more peers.
			spec.RefreshMemberMap(selfIP, selfPID, &memberMap)
			sendMessage(
				newPID,
				fmt.Sprintf("%d,%s", spec.JOINREPLY, spec.EncodeMemberMap(&memberMap)),
			)
		}
	}
}

func listen() {
	p := make([]byte, 2048)

	ser, err := net.ListenUDP("udp", &heartbeatAddr)
	if err != nil {
		log.Fatal(err)
	}

	for {
		_, _, err := ser.ReadFromUDP(p)
		if err != nil {
			log.Fatal(err)
		}

		// Identify appropriate protocol via message code and react
		var bb [][]byte = bytes.Split(p, []byte(","))
		replyCode, err := strconv.Atoi(string(bb[0][0]))
		if err != nil {
			log.Fatal(err)
		}

		switch replyCode {
		// We successfully joined the network
		// Decode the membership gob and merge with our own membership list.
		case spec.JOINREPLY:
			log.Println("[JOINREPLY] Successfully joined network")
			theirMemberMap := spec.DecodeMemberMap(bb[1])
			spec.MergeMemberMaps(&memberMap, &theirMemberMap)
			log.Println(theirMemberMap)
		}
	}
}

func joinNetwork() {
	conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", config.Introducer(), introducerPort))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(conn, "%d,junk0,junk1", spec.JOIN)
	conn.Close()
}

func sendMessage(PID int, message string) {
	// Check to see if that PID is in our membership list
	target, ok := memberMap[PID]
	if !ok {
		log.Fatalf("PID %d not in memberMap", PID)
	}
	conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", target.IP, port))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprint(conn, message)
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
