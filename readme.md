### Long-grep
Golang RPC for grepping logs on multiple machines.
Based on this UIUC425 MP: https://courses.engr.illinois.edu/cs425/fa2019/MP1.CS425.FA19.pdf

## Setup:
1. Setup 5 remote "servers", emulated here using Docker containers.
   Each will have its own set of randomly generated logs and
   distinct port address.
      - `docker-compose up`
      - If you've changed the `dockerfile`, run `docker-compose build` to rebuild the images used for `docker-compose`.
2. Build the client and execute the grep with any arbitrary pattern.
      - `CONFIG=./config.json go run src/main.go -client --expression "include (straight|terrible)"`
# chord-failure-detector
