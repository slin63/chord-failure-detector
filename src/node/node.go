package node

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"../config"
	"../hashing"
	"../spec"
)

var selfIP string
var selfPID int

// [PID:*memberNode]
var memberMap = make(map[int]*spec.MemberNode)

// [PID:Unix timestamp at time of death]
// Assume all PIDs here point to dead nodes, waiting to be deleted
var suspicionMap = make(map[int]int64)

// [finger:PID]
var fingerTable = make(map[int]int)

var joinReplyChan = make(chan int, 10)

const joinInterval = 5
const heartbeatInterval = 5
const garbageInterval = 5

const m int = 7
const introducerPort = 6001
const port = 6000
const delimiter = "//"

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

	// Beat that drum
	go heartbeat()

	// Listen for leaves
	go listenForLeave()

	wg.Wait()
}

// Listen function specifically for JOINs.
func listenForJoins() {
	p := make([]byte, 128)
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
				log.Printf(
					"[COLLISION] PID %v for %v collides with existing node at %v. Try raising m to allocate more ring positions. (m=%v)",
					newPID,
					remoteaddr,
					node.IP,
					m,
				)
			}
			newNode := &spec.MemberNode{
				IP:        remoteaddr.IP.String(),
				Timestamp: time.Now().Unix(),
				Alive:     true,
			}

			spec.SetMemberMap(newPID, newNode, &memberMap)
			spec.RefreshMemberMap(selfIP, selfPID, &memberMap)
			spec.ComputeFingerTable(&fingerTable, &memberMap, selfPID, m)
			log.Printf(
				"[JOIN] (PID=%d) (IP=%s) (T=%d) joined network. Added to memberMap & FT.",
				newPID,
				(*newNode).IP,
				(*newNode).Timestamp,
			)

			// Add message to queue:
			// Send the joiner a membership map so that it can discover more peers.
			joinReplyChan <- newPID
			go func() {
				for range time.Tick(time.Second * time.Duration(joinInterval)) {
					for pid := range joinReplyChan {
						sendMessage(
							pid,
							fmt.Sprintf("%d%s%s", spec.JOINREPLY, delimiter, spec.EncodeMemberMap(&memberMap)),
						)
					}
				}
			}()
		}
	}
}

// Listen function to handle: HEARTBEAT, JOINREPLY
func listen() {
	var p [512]byte

	ser, err := net.ListenUDP("udp", &heartbeatAddr)
	if err != nil {
		log.Fatal("listen(): ", err)
	}

	for {
		n, _, err := ser.ReadFromUDP(p[0:])
		if err != nil {
			log.Fatal(err)
		}

		// Identify appropriate protocol via message code and react
		var bb [][]byte = bytes.Split(p[0:n], []byte(delimiter))
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
			log.Printf(
				"[JOINREPLY] Successfully joined network. Discovered %d peer(s).",
				len(memberMap)-1,
			)
		case spec.HEARTBEAT:
			theirMemberMap := spec.DecodeMemberMap(bb[1])
			lenOld, lenNew := len(memberMap), len(theirMemberMap)
			spec.MergeMemberMaps(&memberMap, &theirMemberMap)
			spec.ComputeFingerTable(&fingerTable, &memberMap, selfPID, m)
			log.Printf(
				"[HEARTBEAT] from PID=%s. (len(memberMap)=%d) (len(suspicionMap)=%d) (lenOld-lenNew=%d)",
				bb[2],
				len(memberMap),
				len(suspicionMap),
				lenOld-lenNew,
			)
		case spec.LEAVE:
			leavingPID, err := strconv.Atoi(string(bb[1]))
			leavingTimestamp, err := strconv.Atoi(string(bb[2]))
			if err != nil {
				log.Fatalf("[LEAVE]: %v", err)
			}

			leaving, ok := memberMap[leavingPID]
			if !ok {
				log.Fatalf("[LEAVE] PID=%s not in memberMap", leavingPID)
			}

			// Add to suspicionMap
			leavingCopy := *leaving
			leavingCopy.Alive = false
			spec.SetSuspicionMap(leavingPID, int64(leavingTimestamp), &suspicionMap)
			spec.SetMemberMap(leavingPID, &leavingCopy, &memberMap)
			log.Printf("[LEAVE] from PID=%d (timestamp=%d)", leavingPID, leavingTimestamp)
		default:
			log.Printf("[NOACTION] Received replyCode: [%d]", replyCode)
		}
	}
}

// Detect ctrl-c signal interrupts and dispatch [LEAVE]s to monitors accordingly
func listenForLeave() {
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		message := fmt.Sprintf(
			"%d%s%d%s%d",
			spec.LEAVE, delimiter,
			selfPID, delimiter,
			time.Now().Unix(),
		)
		log.Print("listenForLeave():", message)
		spec.Disseminate(
			message,
			m,
			selfPID,
			&fingerTable,
			&memberMap,
			sendMessage,
		)
		os.Exit(0)
	}()
}

// Periodically send out heartbeat messages with piggybacked membership map info.
func heartbeat() {
	for range time.Tick(time.Second * time.Duration(heartbeatInterval)) {
		spec.CollectGarbage(
			selfPID,
			garbageInterval,
			m,
			&memberMap,
			&suspicionMap,
			&fingerTable,
		)
		spec.RefreshMemberMap(selfIP, selfPID, &memberMap)
		message := fmt.Sprintf(
			"%d%s%s%s%d",
			spec.HEARTBEAT, delimiter,
			spec.EncodeMemberMap(&memberMap), delimiter,
			selfPID,
		)
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
	fmt.Fprintf(conn, "%d", spec.JOIN)
	conn.Close()
}

func sendMessage(PID int, message string) {
	// Check to see if that PID is in our membership list
	target, ok := memberMap[PID]
	if !ok {
		log.Printf("sendMessage(): PID %d not in memberMap. Skipping.", PID)
		return
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
