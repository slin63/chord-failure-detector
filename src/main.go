package main

import (
	"flag"

	"./hashing"
	"./node"
)

func main() {
	isIntroducerPtr := flag.Bool("introducer", false, "configure as introducer")

	flag.Parse()
	hashing.GetPID("127.0.0.1:8003")

	node.Live(*isIntroducerPtr)
}
