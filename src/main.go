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

	log.Println(isIntroducer)
	node.Live(isIntroducer, logf)
}
