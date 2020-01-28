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
)
const delimiter = ","

type MemberNode struct {
	// Address info formatted ip_address
	IP        string
	Timestamp int64
	Alive     bool
}

func ReportOnline(IP string, PID int, isIntroducer bool) {
	log.Printf("[%s:%d]@%d (INTRODUCER=%v) // ONLINE", IP, PID, time.Now().Unix(), isIntroducer)
}

// TODO We should get mut-exes on things touching the membership map.

// Encode the memberMap for messaging
// https://stackoverflow.com/questions/19762413/how-to-Encode-deEncode-a-map-in-go
func EncodeMemberMap(memberMap *map[int]*MemberNode) []byte {
	b := new(bytes.Buffer)
	e := gob.NewEncoder(b)

	// Encoding the map
	err := e.Encode(memberMap)
	if err != nil {
		log.Fatal(err)
	}
	return b.Bytes()
}

func DecodeMemberMap(b []byte) map[int]*MemberNode {
	buf := bytes.NewBuffer(b)

	var decodedMap map[int]*MemberNode
	d := gob.NewDecoder(buf)

	// Decoding the serialized data
	err := d.Decode(&decodedMap)
	if err != nil {
		log.Fatal(err)
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

	// Generate the indices for the finger table.
	var iths []int
	for i := 0; i < m-1; i++ {
		iths = append(iths, selfPID+(1<<i)%(1<<m))
	}
	sort.Ints(iths)
	// log.Println("doing fingertable FINGERS with selfPID: ", selfPID)
	// log.Println(PIDs)
	// log.Println(iths)

	// Map the indices for each finger table with its corresponding (closest but greater value mod 2^m)
	var last int = 0
	for _, ith := range iths {
		for ; last < len(PIDs); last++ {
			PID := PIDs[last]
			// log.Println("ith", ith)
			// log.Println("PID", PID)

			if (ith - PID) < 0 {
				(*ft)[ith] = PID % (1 << m)
				// log.Printf("found a match! (ith=%v) (PID=%v) (ith - PID = %v)", ith, PID, ith-PID)
				// log.Printf("remaining PIDs: %v", PIDs[last:])
				break
			}
		}
	}
}
