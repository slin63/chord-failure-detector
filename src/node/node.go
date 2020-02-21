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

// [PID:PID] (just using as a hashtable)
var electionMap = make(map[int]int)
var electedMap = make(map[int]int)
var electedConfMap = make(map[int]int)

// [PID:*memberNode]
var memberMap = make(map[int]*spec.MemberNode)

// [PID:Unix timestamp at time of death]
// Assume all PIDs here point to dead nodes, waiting to be deleted
var suspicionMap = make(map[int]int64)

// [finger:PID]
var fingerTable = make(map[int]int)

var joinReplyLen = 10
var joinReplyChan = make(chan int, joinReplyLen)

const joinReplyInterval = 1
const joinAttemptInterval = 20
const heartbeatInterval = 5
const retryElectionInterval = 60
const concludeElectionInterval = 10

const m int = 9
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
		go dispatchJoinReplies()
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
		}
	}
}

// Send out joinReplyLen / 2 [JOINREPLY] messages every joinReplyInterval seconds
func dispatchJoinReplies() {
	for range time.Tick(time.Second * time.Duration(joinReplyInterval)) {
		for i := 0; i < (joinReplyLen / 2); i++ {
			sendMessage(
				<-joinReplyChan,
				fmt.Sprintf("%d%s%s", spec.JOINREPLY, delimiter, spec.EncodeMemberMap(&memberMap)),
				false,
			)
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
				electionMap[theirPID] = theirPID
				if theirPID == selfPID {
					log.Printf("[ELECTED] [PID=%d] won election, disseminating success.", selfPID)
					electionState = spec.AWAITINGQUORUM
					spec.Disseminate(spec.ElectedMessage(delimiter, selfPID), m, selfPID, &fingerTable, &memberMap, sendMessage, true)
				} else if selfPID > theirPID {
					electionForward(spec.ElectionMessage(delimiter, selfPID))
				} else {
					electionForward(string(original))
				}
			} else {
				log.Printf("[ELECTMEREJECT] Ignored [ELECTME] from [PID=%d]!", theirPID)
			}

		// Someone has been elected. Ignore if we've already seen this [ELECTED] message
		// Disseminate the ELECTED message.
		// Let the ELECTED process know we've received the message (to form the quorum)
		case spec.ELECTED:
			if electionState == spec.ELECTING {
				electedPID, _ := strconv.Atoi(string(bb[1]))
				resetElectionStates(electedPID)
				timestamp := bb[2]
				electionState = spec.VOTING
				_, ok := electedMap[electedPID]
				if !ok {
					electedMap[electedPID] = electedPID
					spec.Disseminate(string(original), m, selfPID, &fingerTable, &memberMap, sendMessage, true)
					sendMessage(electedPID, spec.ElectedConfMessage(delimiter, selfPID), true)
					log.Printf("[ELECTED] [PID=%d] [TS=%s]. Disseminated and replied with confirmation!", electedPID, timestamp)
				}
			}

		// We received a election confirmation from someone.
		// Add to our election confirmation map and see if we've met the quorum.
		// if we meet the quorom, disseminate a ELECTIONDONE message
		//   and update our own memberMap entry to reflect the new leadership.
		case spec.ELECTEDCONF:
			if electionState == spec.AWAITINGQUORUM {
				quorum := spec.EvaluateQuorum(&memberMap, &suspicionMap)
				confPID, _ := strconv.Atoi(string(bb[1]))
				electedConfMap[confPID] = confPID
				if len(electedConfMap) >= quorum {
					spec.Disseminate(spec.ElectionDoneMessage(delimiter, selfPID, selfIP), m, selfPID, &fingerTable, &memberMap, sendMessage, true)
					spec.RefreshMemberMap(selfIP, selfPID, &memberMap, true)
					resetElectionStates(selfPID)
					// introducer = true
				} else {
					log.Printf("[ELECTEDCONF] from [PID=%d]. %d/%d votes needed", confPID, len(electedConfMap), quorum)
				}
			}

		// The election is over, clear our election state and reset our election maps
		case spec.ELECTIONDONE:
			if electionState == spec.VOTING {
				// TODO (02/18 @ 16:30): reset election state/maps, make sure new leader actually is working
				//  also need to update introducerAddress = memberMap[newLeaderPID]
				newLeaderPID, _ := strconv.Atoi(string(bb[1]))
				newLeaderIP := string(bb[2])
				resetElectionStates(newLeaderPID)
				memberMap[newLeaderPID].Leader = true
				introducerAddress = newLeaderIP
				log.Printf("[ELECTIONDONE] from [PID=%d]. Cleared election states! ", newLeaderPID)
			}

		default:
			log.Printf("[NOACTION] Received replyCode: [%d]", replyCode)
		}
	}
}

// Reset all election states
func resetElectionStates(leaderPID int) {
	// remove "old" leader from member and suspicion maps
	spec.PurgeOldLeader(leaderPID, &memberMap, &suspicionMap)
	electionState = spec.NOELECTION

	electionMap = make(map[int]int)
	electedMap = make(map[int]int)
	electedConfMap = make(map[int]int)
	electionInitiated = 0
	// time.Sleep(time.Second * concludeElectionState)

}

// Listen function to handle: HEARTBEAT, JOINREPLY
func listen() {
	var p [1024]byte

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

		log.Printf("[PID=%d] [ELECTIONSTATE=%d]", selfPID, electionState)

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
			spec.MergeMemberMaps(&memberMap, &theirMemberMap)
			spec.ComputeFingerTable(&fingerTable, &memberMap, selfPID, m)
			// TODO (02/20 @ 16:15): uncommENT this
			// lenOld, lenNew := len(memberMap), len(theirMemberMap)

			// log.Printf(
			// 	"[HEARTBEAT] from PID=%s. (len(mM)=%d diff(mM)=%d len(sM)=%d) ",
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
				electionForward(spec.ElectionMessage(delimiter, selfPID))
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
