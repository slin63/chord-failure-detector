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
var mux = &sync.Mutex{}

// Update memberMaps / ft whenever we receive a "fresh" copy of the memberMap
var memberMap = make(map[int]*spec.MemberNode)
var fingerTable = make(map[int]int)

var joinReplyChan = make(chan int, 10)
var joinInterval = 5
var heartbeatInterval = 5

const m int = 8
const introducerPort = 6001
const port = 6000
const delimiter = ","

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

	// // Beat that drum
	// go heartbeat()

	// Dispatch buffered messages as needed
	//   - JOINREPLYs
	go dispatchBufferedMessages()

	wg.Wait()
}

// Listen function specifically for JOINs.
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
		var bb [][]byte = bytes.Split(p, []byte(delimiter))
		replyCode, err := strconv.Atoi(string(bb[0][0]))
		if err != nil {
			log.Fatal(err)
		}

		switch replyCode {
		case spec.JOIN:
			// Update our own member map & fts
			newPID := hashing.GetPID(remoteaddr.IP.String(), m)

			// Check for potential collisions / outdated memberMap
			if node, exists := memberMap[newPID]; exists {
				log.Fatalf(
					"[COLLISION] PID %v for %v collides with existing node at %v. Try raising m to allocate more ring positions. (m=%v)",
					newPID,
					remoteaddr,
					node.IP,
					m,
				)
			}

			memberMap[newPID] = &spec.MemberNode{
				IP:        remoteaddr.IP.String(),
				Timestamp: time.Now().Unix(),
				Alive:     true,
			}
			spec.RefreshMemberMap(selfIP, selfPID, &memberMap)
			spec.ComputeFingerTable(&fingerTable, &memberMap, selfPID, m)
			log.Printf(
				"[JOIN] (PID=%d) (IP=%s) (T=%d) joined network. Added to memberMap & FT.",
				newPID,
				(*memberMap[newPID]).IP,
				(*memberMap[newPID]).Timestamp,
			)

			// Add message to queue:
			// Send the joiner a membership map so that it can discover more peers.
			joinReplyChan <- newPID
		}
	}
}

// Listen function to handle:
//   - HEARTBEAT
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
		var bb [][]byte = bytes.Split(p, []byte(delimiter))
		replyCode, err := strconv.Atoi(string(bb[0][0]))
		if err != nil {
			log.Fatal(err)
		}

		switch replyCode {
		// We successfully joined the network
		// Decode the membership gob and merge with our own membership list.
		case spec.JOINREPLY:
			theirMemberMap := spec.DecodeMemberMap(bb[1])
			spec.MergeMemberMaps(&memberMap, &theirMemberMap)
			spec.ComputeFingerTable(&fingerTable, &memberMap, selfPID, m)
			log.Printf("[JOINREPLY] Successfully joined network. Discovered %d peer(s).", len(memberMap)-1)
		}
	}
}

func dispatchBufferedMessages() {
	go func() {
		for range time.Tick(time.Second * time.Duration(joinInterval)) {
			for pid := range joinReplyChan {
				sendMessage(
					pid,
					fmt.Sprintf("%d,%s", spec.JOINREPLY, spec.EncodeMemberMap(&memberMap)),
				)
			}
		}
	}()
}

// Periodically send out heartbeat messages with piggybacked membership map info.
func heartbeat() {
	for range time.Tick(time.Second * time.Duration(heartbeatInterval)) {
		spec.RefreshMemberMap(selfIP, selfPID, &memberMap)
		message := fmt.Sprintf("%d,%s", spec.HEARTBEAT, spec.EncodeMemberMap(&memberMap))
		spec.Disseminate(
			message,
			m,
			selfPID,
			&fingerTable,
			&memberMap,
			sendMessage,
		)
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
