package spec

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"log"
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

	fmt.Printf("%#v\n", decodedMap)
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
func MergeMemberMaps(ours, theirs *map[int]*MemberNode) {
	// TODO
}
