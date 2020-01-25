package main

import (
	"flag"

	"./node"
)

const logf = "o.log"

func main() {
	isIntroducerPtr := flag.Bool("introducer", false, "configure as introducer")
	flag.Parse()

	node.Live(*isIntroducerPtr, logf)
}
