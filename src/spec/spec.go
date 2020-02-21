package spec

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"log"
	"math"
	"sort"
	"sync"
	"time"

	"../sem"
)

const (
	// Message codes
	JOIN = iota
	JOINREPLY
	LEAVE
	HEARTBEAT
	// Election messages
	ELECTME      // Self election message
	ELECTED      // Message with elected candidate
	ELECTEDCONF  // Message to confirm to elected candidate
	ELECTIONDONE // Message with new leader info that resets election states and memtables.

	// Election states
	NOELECTION     // No election currently underway
	ELECTING       // Selecting a leader
	VOTING         // Agreeing that the elected leader is fit.
	AWAITINGQUORUM // The leader awaiting the quorum
)

const timeFail = 15
const timeCleanup = 20

var quorum = 0.6

// Globally deny access to certain memberMap & suspicionMap operations.
var memberMapSem = make(sem.Semaphore, 1)
var suspicionMapSem = make(sem.Semaphore, 1)

// Mutex to deny access to stretches of code
var mux = &sync.Mutex{}

type MemberNode struct {
	// Address info formatted ip_address
	IP        string
	Leader    bool
	Timestamp int64
	Alive     bool
}

func ReportOnline(IP string, PID int, isIntroducer bool) {
	log.Printf("[ONLINE] [PID=%d] [%s]@%d (INTRODUCER=%v)", PID, IP, time.Now().Unix(), isIntroducer)
}

// Encode the memberMap for messaging
// https://stackoverflow.com/questions/19762413/how-to-Encode-deEncode-a-map-in-go
func EncodeMemberMap(memberMap *map[int]*MemberNode) []byte {
	b := new(bytes.Buffer)
	e := gob.NewEncoder(b)

	// Encoding the map
	err := e.Encode(*memberMap)
	if err != nil {
		log.Fatal("EncodeMemberMap():", err)
	}
	return b.Bytes()
}

func PurgeOldLeader(leaderPID int, memberMap *map[int]*MemberNode, suspicionMap *map[int]int64) int {
	// safely remove leader from suspicion map and member map
	var oldLeaderPID int
	memberMapSem.Lock()
	for PID, member := range *memberMap {
		if member.Leader == true && PID != leaderPID {
			oldLeaderPID = PID
		}
	}
	suspicionMapSem.Lock()
	delete(*memberMap, oldLeaderPID)
	delete(*suspicionMap, oldLeaderPID)
	memberMapSem.Unlock()
	suspicionMapSem.Unlock()
	return oldLeaderPID
}

func DecodeMemberMap(b []byte) map[int]*MemberNode {
	buf := bytes.NewBuffer(b)
	gob.Register(MemberNode{})

	var decodedMap map[int]*MemberNode
	d := gob.NewDecoder(buf)

	// Decoding the serialized data
	err := d.Decode(&decodedMap)
	if err != nil {
		log.Fatal("DecodeMemberMap():", err)
	}

	return decodedMap
}

func SetMemberMap(k int, v *MemberNode, memberMap *map[int]*MemberNode) {
	memberMapSem.Lock()
	(*memberMap)[k] = v
	memberMapSem.Unlock()
}

// Refresh the self node's entry inside the membership table
func RefreshMemberMap(selfIP string, selfPID int, memberMap *map[int]*MemberNode, leader bool) {
	memberMapSem.Lock()
	(*memberMap)[selfPID] = &MemberNode{
		IP:        selfIP,
		Timestamp: time.Now().Unix(),
		Alive:     true,
		Leader:    leader,
	}
	memberMapSem.Unlock()
}

// Merge two membership maps, preserving entries with the latest timestamp
// Something in theirs but not in ours?
//   - timestamp(theirs) > timestamp(ours) => keep
//   - alive(theirs) == false => update ours.alive
func MergeMemberMaps(ours, theirs *map[int]*MemberNode) {
	memberMapSem.Lock()
	for PID, node := range *theirs {
		_, exists := (*ours)[PID]
		if exists {
			if (*theirs)[PID].Timestamp > (*ours)[PID].Timestamp {
				(*ours)[PID] = node
			}
		} else {
			(*ours)[PID] = node
		}
	}
	memberMapSem.Unlock()
}

func SetSuspicionMap(k int, v int64, suspicionMap *map[int]int64) {
	suspicionMapSem.Lock()
	(*suspicionMap)[k] = v
	suspicionMapSem.Unlock()
}

func ComputeFingerTable(ft *map[int]int, memberMap *map[int]*MemberNode, selfPID, m int) {
	mux.Lock()
	// Get all PIDs and extend them with themselves + 2^m so that they "wrap around".
	var PIDs []int
	var PIDsExtended []int
	for PID := range *memberMap {
		if PID != selfPID {
			PIDs = append(PIDs, PID)
			PIDsExtended = append(PIDsExtended, PID+(1<<m))
		}
	}
	PIDs = append(PIDs, PIDsExtended...)
	sort.Ints(PIDs)

	// Populate the finger table.
	var last int = 0
	for i := 0; i < m-1; i++ {
		ith := selfPID + (1<<i)%(1<<m)
		for ; last < len(PIDs); last++ {
			PID := PIDs[last]

			if (ith - PID) < 0 {
				(*ft)[ith] = PID % (1 << m)
				break
			}
		}
	}
	mux.Unlock()
}

// Periodically compare our suspicion array & memberMap and remove
// nodes who have been dead for a sufficiently long time
// from https://courses.physics.illinois.edu/cs425/fa2019/L6.FA19.pdf
// If the heartbeat has not increased for more than Tfail [s], the member is considered failed
// And after a further Tcleanup [s], it will delete the member from the list
func CollectGarbage(
	selfPID, m int,
	memberMap *map[int]*MemberNode,
	suspicionMap *map[int]int64,
	fingerTable *map[int]int,
) {
	nodesToDelete := []int{}
	nodesToRevive := []int{}
	now := time.Now().Unix()

	// Check for dying members in memberMap, add to suspicion map to cleanup
	// Lock up memberMap here because we're iterating over it.
	memberMapSem.Lock()
	suspicionMapSem.Lock()
	for PID, nodePtr := range *memberMap {
		if PID == selfPID {
			continue
		}
		timestamp := (*nodePtr).Timestamp

		// This node is dead. Add to suspicionMap.
		if (now - timestamp) >= timeFail {
			if _, ok := (*suspicionMap)[PID]; !ok {
				(*nodePtr).Alive = false
				(*suspicionMap)[PID] = now
				log.Printf("[FAILURE] CollectGarbage(0) Node (PID=%v) added to suspicionMap", PID)
			}
		}
	}

	// Finally bury sufficiently rotted nodes.
	for PID, timestamp := range *suspicionMap {
		nodePtr := (*memberMap)[PID]

		// Either revive a rejoined node OR assume that word of this node's death has been disseminated and forget it.
		if (*nodePtr).Alive {
			nodesToRevive = append(nodesToRevive, PID)
		} else if (now - timestamp) >= timeCleanup {
			nodesToDelete = append(nodesToDelete, PID)
		}
	}

	// Write to suspicion and member maps, update fingerTable so we don't try to disseminate to dead nodes.
	for _, PID := range nodesToRevive {
		delete(*suspicionMap, PID)
	}
	for _, PID := range nodesToDelete {
		log.Printf("[FAILURECLEAN] Node (PID=%v) removed from memberMap", PID)
		delete(*memberMap, PID)
		delete(*suspicionMap, PID)
	}
	ComputeFingerTable(fingerTable, memberMap, selfPID, m)
	memberMapSem.Unlock()
	suspicionMapSem.Unlock()
}

func Disseminate(
	message string,
	m int,
	selfPID int,
	fingertable *map[int]int,
	memberMap *map[int]*MemberNode,
	sendMessage func(int, string, bool),
	election bool,
) {
	if len(*memberMap) > 1 {
		// Identify predecessor & 2 successors, or less if not available
		monitors := GetMonitors(selfPID, m, memberMap)

		// Mix monitors with targets in fingertable
		targets := GetTargets(selfPID, fingertable, &monitors)
		for _, PID := range targets {
			sendMessage(PID, message, election)
		}
	}
}

func GetTargets(selfPID int, fingertable *map[int]int, monitors *[]int) []int {
	var targets []int
	for _, PID := range *fingertable {
		// NOT its own PID AND monitors DOESN'T contain this PID AND targets DOESN'T contain this PID
		if PID != selfPID && (index(*monitors, PID) == -1) && (index(targets, PID) == -1) {
			targets = append(targets, PID)
		}
	}

	return append(targets, *monitors...)
}

// Identify the PID of node [pred, succ1, succ2] (in that order)
func GetMonitors(selfPID, m int, memberMap *map[int]*MemberNode) []int {
	// Get all PIDs and extend them with themselves + 2^m so that they "wrap around".
	var PIDs []int
	var PIDsExtended []int
	for PID := range *memberMap {
		PIDs = append(PIDs, PID)
		PIDsExtended = append(PIDsExtended, PID+(1<<m))
	}
	PIDs = append(PIDs, PIDsExtended...)
	sort.Ints(PIDs)

	// Predecessor PID is PID directly behind the selfPID in the extended ring
	// Successor PID directly ahead, and so forth
	// (1 << m == 2^m)
	var monitors []int
	log.Println("GetMonitors(A): selfPID=", selfPID)
	log.Println("GetMonitors(B): selfPID+(1<<m)=", selfPID+(1<<m))
	log.Println("GetMonitors(C): PIDs=", PIDs)
	selfIdx := index(PIDs, selfPID+(1<<m))
	predIdx := (selfIdx - 1) % len(PIDs)
	succIdx := (selfIdx + 1) % len(PIDs)
	succ2Idx := (selfIdx + 2) % len(PIDs)
	log.Println("GetMonitors(D): selfIdx=", selfIdx)

	for _, idx := range []int{predIdx, succIdx, succ2Idx} {
		PID := PIDs[idx] % (1 << m)
		if index(monitors, PID) == -1 && PID != selfPID {
			monitors = append(monitors, PID)
		}
	}
	log.Println("GetMonitors(F): monitors=", monitors)

	return monitors
}

// Identify the PID of node [pred, succ1, succ2] (in that order)
func GetSuccPIDWithoutLeader(selfPID, m int, memberMap *map[int]*MemberNode) int {
	var memberMapNoLeader = make(map[int]*MemberNode)
	for k, v := range *memberMap {
		if !v.Leader {
			memberMapNoLeader[k] = v
		}
	}
	return GetMonitors(selfPID, m, &memberMapNoLeader)[1]
}

func EvaluateQuorum(memberMap *map[int]*MemberNode, suspicionMap *map[int]int64) int {
	// (len(memberMap) - 1) because member map includes the self node as well
	return int(math.Floor(
		quorum * float64(((len(*memberMap) - 1) - len(*suspicionMap))),
	))
}

func ElectionMessage(delimiter string, selfPID int) string {
	return fmt.Sprintf("%d%s%d", ELECTME, delimiter, selfPID)
}

func ElectedMessage(delimiter string, selfPID int) string {
	return fmt.Sprintf("%d%s%d%s%d", ELECTED, delimiter, selfPID, delimiter, time.Now().Unix)
}

func ElectedConfMessage(delimiter string, selfPID int) string {
	return fmt.Sprintf("%d%s%d", ELECTEDCONF, delimiter, selfPID)
}

func ElectionDoneMessage(delimiter string, selfPID int, selfAddress string) string {
	return fmt.Sprintf("%d%s%d", ELECTIONDONE, delimiter, selfPID, delimiter, selfAddress)
}

func index(a []int, val int) int {
	for i, v := range a {
		if v == val {
			return i
		}
	}
	return -1
}

func FmtMemberMap(selfPID int, m *map[int]*MemberNode) string {
	memberMapSem.Lock()
	var o = "\n----------------------\n"
	for PID, nodePtr := range *m {
		if selfPID == PID {
			o += fmt.Sprintf("* PID %v: Time: %v Alive: %v Leader: %v\n", PID, (*nodePtr).Timestamp, (*nodePtr).Alive, (*nodePtr).Leader)
		} else {
			o += fmt.Sprintf("  PID %v: Time: %v Alive: %v Leader: %v\n", PID, (*nodePtr).Timestamp, (*nodePtr).Alive, (*nodePtr).Leader)
		}
	}
	o += "----------------------\n"
	memberMapSem.Unlock()
	return o
}
