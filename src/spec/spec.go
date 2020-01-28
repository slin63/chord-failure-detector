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
	HEARTBEAT
)

type MemberNode struct {
	// Address info formatted ip_address
	IP        string
	Timestamp int64
	Alive     bool
}

// func init() {
// 	gob.Register(MemberNode{})
// }

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
	log.Printf("EncodeMemberMap(): %s", b.String())
	return b.Bytes()
}

func DecodeMemberMap(b []byte) map[int]*MemberNode {
	log.Printf("DecodeMemberMap(): (len(b) = %v)", len(b))
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
	for k, v := range *theirs {
		_, exists := (*ours)[k]
		if exists {
			if (*theirs)[k].Timestamp > (*ours)[k].Timestamp {
				(*ours)[k] = v
			}
		} else {
			(*ours)[k] = v
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
		log.Println("Disseminate(): monitors", monitors)
		log.Println("target", targets)
		// TODO: uncomment
		// for _, PID := range targets {
		// 	sendMessage(PID, message)
		// }
	}
}

func GetTargets(selfPID int, fingertable *map[int]int, monitors *[]int) []int {
	var targets []int
	for _, PID := range *fingertable {
		if PID != selfPID && (index(*monitors, PID) != -1) && (index(targets, PID) != -1) {
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

	// log.Printf("GetMonitors(): (monitors=%v)", monitors)
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
