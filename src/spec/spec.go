package spec

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

const (
	JOIN = iota
	MEMBERSHIP
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

// Serialize the memberMap for messaging
// https://stackoverflow.com/questions/19762413/how-to-serialize-deserialize-a-map-in-go
func SerializeMemberMap(memberMap *map[int]*MemberNode) []byte {
	b := new(bytes.Buffer)
	e := gob.NewEncoder(b)

	// Encoding the map
	err := e.Encode(memberMap)
	if err != nil {
		panic(err)
	}
	return b.Bytes()
}

// Refresh the self node's entry inside the membership table
func RefreshMemberMap(selfIP string, selfPID int, memberMap *map[int]*MemberNode) {
	(*memberMap)[selfPID] = &MemberNode{
		IP:        selfIP,
		Timestamp: time.Now().Unix(),
		Alive:     true,
	}
}

// Compare a string to an ennumerable
func C(s string, enum int) bool {
	sc, err := strconv.Atoi(s)
	if err != nil {
		log.Fatal("Invalid message code: ", s)
	}
	return sc == enum
}

// Parse a []byte to a string array
func P(b []byte) []string {
	return strings.Split(fmt.Sprintf("%s", b), delimiter)
}
