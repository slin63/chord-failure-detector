package spec

import (
	"bytes"
	"encoding/gob"
	"log"
	"sort"
	"time"
)

const (
	JOIN = iota
	JOINREPLY
	LEAVE
	HEARTBEAT
)

const timeFail = 5
const timeCleanup = 5

type MemberNode struct {
	// Address info formatted ip_address
	IP        string
	Timestamp int64
	Alive     bool
}

func ReportOnline(IP string, PID int, isIntroducer bool) {
	log.Printf("[%s:%d]@%d (INTRODUCER=%v) // ONLINE", IP, PID, time.Now().Unix(), isIntroducer)
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

// Refresh the self node's entry inside the membership table
func RefreshMemberMap(selfIP string, selfPID int, memberMap *map[int]*MemberNode) {
	(*memberMap)[selfPID] = &MemberNode{
		IP:        selfIP,
		Timestamp: time.Now().Unix(),
		Alive:     true,
	}
}

// Merge two membership maps, preserving entries with the latest timestamp
// Something in theirs but not in ours?
//   - timestamp(theirs) > timestamp(ours) => keep
//   - alive(theirs) == false => update ours.alive
func MergeMemberMaps(ours, theirs *map[int]*MemberNode) {
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
}

func ComputeFingerTable(ft *map[int]int, memberMap *map[int]*MemberNode, selfPID, m int) {
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
}

// Periodically compare our suspicion array & memberMap and remove
// nodes who have been dead for a sufficiently long time
// from https://courses.physics.illinois.edu/cs425/fa2019/L6.FA19.pdf
// 4FailureDetection.mp4 -1:07 (??)
// If the heartbeat has not increased for more than Tfail [s], the member is considered failed
//     --> Mark node.Cleanup = True
// And after a further Tcleanup [s], it will delete the member from the list
//     --> spec.CollectGarbage() removes from memberMap
func CollectGarbage(selfPID, interval int, memberMap *map[int]*MemberNode, suspicionMap *map[int]int64) {
	for range time.Tick(time.Second * time.Duration(interval)) {
		toDelete := []int{}
		now := time.Now().Unix()
		// Check for dying members in memberMap, add to suspicion map to cleanup
		for PID, nodePtr := range *memberMap {
			if PID == selfPID {
				continue
			}

			timestamp := (*nodePtr).Timestamp

			log.Printf("CollectGarbage(-1): (PID=%v) (now-timestamp=%v)", PID, (now - timestamp))

			// This node is dead. Add to suspicionMap, else revive nodes that were wrongfully put to death
			if (now-timestamp) >= timeFail && (*nodePtr).Alive {
				(*nodePtr).Alive = false
				(*suspicionMap)[PID] = now
				log.Printf("CollectGarbage(0): Node (PID=%v) added to suspicionMap", PID)
			} else if (now-timestamp) <= 0 && !(*nodePtr).Alive {
				(*nodePtr).Alive = true
				delete(*suspicionMap, PID)
				log.Printf("CollectGarbage(1): Node (PID=%v) revived & removed from suspicionMap", PID)
			}
		}

		// Finally bury sufficiently rotted nodes.
		for PID, timestamp := range *suspicionMap {
			nodePtr := (*memberMap)[PID]
			log.Printf("CollectGarbage(1.5): Accessing .Alive on %v (PID=%v)", *nodePtr, PID)
			if (*nodePtr).Alive {
				log.Fatalf("CollectGarbage(2): Node (PID=%d) is alive and in suspicionMap, but should be dead.", PID)
			}

			// We can assume that word of this node's death has been disseminated. Time to forget!
			if (now - timestamp) >= timeCleanup {
				toDelete = append(toDelete, PID)
			}
		}

		for _, PID := range toDelete {
			delete(*memberMap, PID)
			delete(*suspicionMap, PID)
			log.Printf("CollectGarbage(3): (PID=%v) has been put to death. (len(memberMap)=%v)", PID, len(*memberMap))
		}

		log.Printf("collectGarbage(4): (suspicionMap=%v) (len(memberMap)=%v)", suspicionMap, len(*memberMap))
	}
}

func Disseminate(
	message string,
	m int,
	selfPID int,
	fingertable *map[int]int,
	memberMap *map[int]*MemberNode,
	sendMessage func(int, string),
) {
	if len(*memberMap) > 1 {
		// Identify predecessor & 2 successors, or less if not available
		monitors := GetMonitors(selfPID, m, memberMap)

		// Mix monitors with targets in fingertable
		targets := GetTargets(selfPID, fingertable, &monitors)
		for _, PID := range targets {
			sendMessage(PID, message)
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

// Identify the PID of node directly behind the self node
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
	selfIdx := index(PIDs, selfPID+(1<<m))
	predIdx := (selfIdx - 1) % len(PIDs)
	succIdx := (selfIdx + 1) % len(PIDs)
	succ2Idx := (selfIdx + 2) % len(PIDs)

	for _, idx := range []int{predIdx, succIdx, succ2Idx} {
		PID := PIDs[idx] % (1 << m)
		if index(monitors, PID) == -1 && PID != selfPID {
			monitors = append(monitors, PID)
		}
	}

	return monitors
}

func index(a []int, val int) int {
	for i, v := range a {
		if v == val {
			return i
		}
	}
	return -1
}
