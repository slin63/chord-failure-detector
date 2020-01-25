package main

import (
	"./hashing"
)

func main() {
	hashing.GetPID("127.0.0.1:8000")
	hashing.GetPID("127.0.0.1:8001")
	hashing.GetPID("127.0.0.1:8002")
	hashing.GetPID("127.0.0.1:8003")
}
