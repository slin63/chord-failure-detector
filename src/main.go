package main

import (
	"flag"
	"log"
	"os"
	"strconv"

	"./node"
)

const logf = "o.log"

func main() {
	isIntroducer, err := strconv.ParseBool(os.Getenv("INTRODUCER"))
	if err != nil {
		log.Fatal("INTRODUCER not set in this environment")
	}
	flag.Parse()

	node.Live(isIntroducer, logf)
}

// docker run --network failure-detector_default failure-detector_pizza
// docker run -e INTRODUCER=1 --network failure-detector_default failure-detector_introducer:latest
// docker run --network failure-detector_default failure-detector_pizza
