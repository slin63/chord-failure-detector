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
var electionState int
var electionInitiated int64
var introducerAddress string

// [PID:timestamp]
var electionMap = make(map[int]int64)
var electedMap = make(map[int]int)

// [PID:*memberNode]
var memberMap = make(map[int]*spec.MemberNode)

// [PID:Unix timestamp at time of death]
// Assume all PIDs here point to dead nodes, waiting to be deleted
var suspicionMap = make(map[int]int64)

// [finger:PID]
var fingerTable = make(map[int]int)

var joinReplyChan = make(chan int, 10)

const joinReplyInterval = 5
const joinAttemptInterval = 20
const heartbeatInterval = 5
const retryElectionInterval = 60

const m int = 7
const electionPort = 6002
const introducerPort = 6001
const port = 6000
const delimiter = "//"

var heartbeatAddr net.UDPAddr
var electionAddr net.UDPAddr

func Live(introducer bool, logf string) {
	electionState = spec.NOELECTION
	introducerAddress = config.Introducer()
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
	electionAddr = net.UDPAddr{
		IP:   net.ParseIP(selfIP),
		Port: electionPort,
	}

	// Join the network if you're not the introducer
	if !introducer {
		joinNetwork()
	} else {
		go listenForJoins()
	}

	// Beat that drum
	go heartbeat(introducer)

	// Listen for messages
	go listen()

	// Listen for election messages
	go listenForElections()

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
			spec.RefreshMemberMap(selfIP, selfPID, &memberMap, true)
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
				for range time.Tick(time.Second * time.Duration(joinReplyInterval)) {
					for pid := range joinReplyChan {
						sendMessage(
							pid,
							fmt.Sprintf("%d%s%s", spec.JOINREPLY, delimiter, spec.EncodeMemberMap(&memberMap)),
							false,
						)
					}
				}
			}()
		}
	}
}

// Listen function to handle: ELECTME, ELECTED
func listenForElections() {
	var p [256]byte

	ser, err := net.ListenUDP("udp", &electionAddr)
	if err != nil {
		log.Fatal("listenForElections(): ", err)
	}

	for {
		n, _, err := ser.ReadFromUDP(p[0:])
		if err != nil {
			log.Fatal(err)
		}

		// Identify appropriate protocol via message code and react
		var original = p[0:n]
		var bb [][]byte = bytes.Split(p[0:n], []byte(delimiter))
		replyCode, err := strconv.Atoi(string(bb[0][0]))
		if err != nil {
			log.Fatal(err)
		}

		switch replyCode {
		// We received an election message from someone else.
		case spec.ELECTME:
			theirPID, _ := strconv.Atoi(string(bb[1]))

			// Ignore recently received election messages
			// if their PID == our PID, we won! Disseminate an ELECTED message and wait for a consensus
			// if their PID > our PID, forward original ELECTME message. Otherwise, replace with our own message
			// otherwise, forward the ELECTME message
			_, ok := electionMap[theirPID]
			if !ok {
				log.Printf("[ELECTMEACK] Got [ELECTME] with [PID=%d]!", theirPID)
				electionMap[theirPID] = time.Now().Unix()
				if theirPID == selfPID {
					log.Printf("[ELECTED] [PID=%d] won election, disseminating success.", selfPID)
					electionState = spec.AWAITINGQUORUM
					spec.Disseminate(electedMessage(), m, selfPID, &fingerTable, &memberMap, sendMessage, true)
				} else if selfPID > theirPID {
					electionForward(electionMessage())
				} else {
					electionForward(string(original))
				}
			} else {
				log.Printf("[ELECTMEREJECT] Ignored [ELECTME] from [IP=%s] [PID=%d]!", theirAddress, theirPID)
			}

		// Someone has been elected. Ignore if we've already seen this [ELECTED] message
		// Disseminate the ELECTED message.
		// Let the ELECTED process know we've received the message (to form the quorum)
		case spec.ELECTED:
			electedPID, _ := strconv.Atoi(string(bb[1]))
			timestamp := bb[2]
			electionState = spec.VOTING
			_, ok := electedMap[electedPID]
			if !ok {
				electedMap[electedPID] = electedPID
				spec.Disseminate(string(original), m, selfPID, &fingerTable, &memberMap, sendMessage, true)
				sendMessage(electedPID, electedConfMessage(), true)
				log.Printf("[ELECTED] [PID=%d] [TS=%s]. Disseminated and replied with confirmation!", electedPID, timestamp)
			}

		// We received a election confirmation from someone.
		// Add to our election confirmation map and see if we've met the quorum.
		// if we meet the quorom, disseminate a ELECTIONDONE message and PARTY
		// TODO: implement comments from above
		case spec.ELECTEDCONF:
			confPID, _ := strconv.Atoi(string(bb[1]))
			log.Printf("[ELECTEDCONF] Got confirmation from [PID=%d]!", confPID)

		default:
			log.Printf("[NOACTION] Received replyCode: [%d]", replyCode)
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
			// lenOld, lenNew := len(memberMap), len(theirMemberMap)
			spec.MergeMemberMaps(&memberMap, &theirMemberMap)
			spec.ComputeFingerTable(&fingerTable, &memberMap, selfPID, m)
			// # TODO: uncomment
			// log.Printf(
			// 	"[HEARTBEAT] from PID=%s. (len(memberMap)=%d diff(memberMap)=%d) (len(suspicionMap)=%d) ",
			// 	bb[2],
			// 	len(memberMap),
			// 	lenOld-lenNew,
			// 	len(suspicionMap),
			// )
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

			// Add to suspicionMap so that none-linked nodes will eventually hear about this.
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
		spec.Disseminate(
			message,
			m,
			selfPID,
			&fingerTable,
			&memberMap,
			sendMessage,
			false,
		)
		os.Exit(0)
	}()
}

// Periodically send out heartbeat messages with piggybacked membership map info.
// Make sure that the introducer isn't dead and begin a leader election if they are
func heartbeat(introducer bool) {
	for {
		if len(memberMap) == 1 && !introducer {
			log.Printf("[ORPHANED]: [SELFPID=%d] attempting to reconnect with introducer to find new peers.", selfPID)
			joinNetwork()
		}
		if !introducer {
			// Check for Leader liveness
			checkLeaderLiveness()
		}
		spec.CollectGarbage(
			selfPID,
			m,
			&memberMap,
			&suspicionMap,
			&fingerTable,
		)
		spec.RefreshMemberMap(selfIP, selfPID, &memberMap, introducer)
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
			false,
		)
		time.Sleep(time.Second * heartbeatInterval)
	}
}

// Check the leader is alive. If not, initiate a new election
func checkLeaderLiveness() {
	// Only check if it's been sufficient time since we last checked
	if time.Now().Unix()-electionInitiated > retryElectionInterval {
		conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", introducerAddress, introducerPort))
		if err != nil {
			if electionState == spec.NOELECTION {
				log.Println(spec.FmtMemberMap(selfPID, &memberMap))
				log.Println("[ELECTME] Leader dead! Initiating election. ")
				electionInitiated = time.Now().Unix()
				electionForward(electionMessage())
			}

		} else {
			conn.Close()
		}
	}

}

// Formats the election message, sets our Electing state = 1, sends message off to nearest neighbor
func electionForward(message string) {
	if len(memberMap) > 1 {
		succ1 := spec.GetSuccPIDWithoutLeader(selfPID, m, &memberMap)
		if succ1 != selfPID {
			sendMessage(succ1, message, true)
			electionState = spec.ELECTING
		}
	} else {
		log.Println("[ELECTFAIL] No other peers to run election with.")
	}
}

func electionMessage() string {
	return fmt.Sprintf("%d%s%s%s%d", spec.ELECTME, delimiter, selfPID)
}

func electedMessage() string {
	return fmt.Sprintf("%d%s%d%s%d", spec.ELECTED, delimiter, selfPID, delimiter, time.Now().Unix)
}

func electedConfMessage() string {
	return fmt.Sprintf("%d%s%d%s%d", spec.ELECTEDCONF, delimiter, selfPID, delimiter)
}

func joinNetwork() {
	for {
		conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", introducerAddress, introducerPort))
		if err != nil {
			log.Printf("[ERROR] Unable to connect to introducer. Trying again in %d seconds.", joinAttemptInterval)
			time.Sleep(time.Second * joinAttemptInterval)
		} else {
			fmt.Fprintf(conn, "%d", spec.JOIN)
			conn.Close()
			return
		}
	}
}

func sendMessage(PID int, message string, election bool) {
	// Check to see if that PID is in our membership list
	var targetPort int
	target, ok := memberMap[PID]
	if !ok {
		log.Printf("sendMessage(): PID %d not in memberMap. Skipping.", PID)
		return
	}
	if election {
		targetPort = electionPort
	} else {
		targetPort = port
	}
	conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", target.IP, targetPort))
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
