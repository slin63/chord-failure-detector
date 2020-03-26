# Chord-ish ![](./images/chord.png)

A play implementation of the Chord protocol as described in [this paper](https://pdos.csail.mit.edu/papers/ton:chord/paper-ton.pdf) for use as the membership and failure detection layers for [*Chord-ish DeFiSh*](https://github.com/slin63/chord-dfs).

## Chord, briefly

### Membership & Failure Detection

In any distributed system there needs to be some way of knowing what other nodes (machines / processes) are members within a group. A *membership layer* achieves that by implementing some protocol that provides a consistent view of memberships within that group, thereby allowing nodes to discover and communicate with one another.  

Also, as in any distributed system, failures are inevitable. Consequently, membership layers need to also be able to adapt to unexpected changes in membership. This requires a failure detection component, assigned with the task of detecting node failures or crashes. 

### Introducing, Chord-ish

To fill the role of the *membership* and *failure detection* layers for my highly convoluted and inefficient distributed key/value store, [Chord-ish DeFiSh](https://github.com/slin63/chord-dfs).  I decided to ~~implement~~ bastardize *[Chord](https://pdos.csail.mit.edu/papers/ton:chord/paper-ton.pdf)*. 

Why did I do this instead of building something way simpler, such as an all to all, all to one, or barebones gossip membership protocol? Because I saw Chord in a UIUC-CS425 lecture on peer-to-peer systems and thought it sounded really cool.

Many features were stripped out and many poor decisions were made, and my following description of the protocol will be specific to my usage of it and *should not* be trusted under any circumstances. I'll use *Chord-ish* to identify my implementation.

Let's begin!

### Consistent Hashing & Finger Tables

First, some definitions and context. 

*Consistent hashing* is the process by which a hash function is used to distribute *K* objects across *N* points on a virtual ring, where *N = 2<sup>m</sup> - 1*. Here, *m* is an arbitrary constant that can be made larger or smaller, correlating directly with the expected number of nodes that the protocol will be supporting. 

Why is this useful? Let's say that we have a distributed key/value store that assigns server positions and key/value assignments with the following formulas: 

```go
assignedPoint := hash(IPAddr) % (Number of servers)
assignedServer := hash(key) % (Number of servers)
```

Servers and key/value pairs are still evenly distributed here, yes, but if the number of servers is ever changed for something like a horizontal up or downscale, downtime is needed to rehash nodes and key/value pairs on to newly evaluated `assignedPoints` and `assignedServers`. Consistent hashing assigns inputs to points using a function that is independent of the total number of node within a group, meaning we can scale up or down without needing downtime to rehash.

Chord-ish works by using a *consistent hashing* function to assign the IP addresses (or any unique identifier) of an arbitrary number of nodes onto a virtual ring with 2<sup>m</sup> - 1 points. Consistent hashing ideally distributes all nodes evenly across the ring, which leads to some built-in load-balancing which can be useful when using Chord-ish's node's as key/value stores, as we do in Chord-ish DeFiSh. 

Chord-ish's consistent hashing function is implemented in `/internal/hashing`.

```go
func MHash(address string, m int) int {
	// Create a new SHA1 hasher 
  h := sha1.New() 
  
  // Write our address to the hasher
	if _, err := h.Write([]byte(address)); err != nil {
		log.Fatal(err)
	}
	b := h.Sum(nil)

	// Truncate our 160 bit hash down to m bits.
	pid := binary.BigEndian.Uint64(b) >> (64 - m)

  // Convert to an integer to get our ring "point", which
  // doubles as our process/node ID.
	return int(pid)
}
```

After a node is assigned onto the ring, it generates a *finger table*. Finger tables are Chord-ish's routing tables, key/value maps of length *m* where the corresponding keys and values are as follows

- `key = some integer *i* ranging from (0, m)`
- `value = (first node with PID >= (currentNode.PID + 2**(i - 1)) % (2 ** m))`.

Finger tables are used in canonical Chord to enable *O(log n)* lookups from node to node, but because Chord-ish won't actually be doing any storing of its own, finger tables here are only used to disseminate heartbeat and graceful termination messages. Doesn't that defeat the purpose of going through all the trouble of implementing Chord? Haha! Yes. No one's paying me to do this so I can do a bad job if I want. Not that I wouldn't do a bad job if someone was paying me, either.

Below is a diagram that demonstrates what's been described so far. A node is born into this world, hashed onto a point onto the ring, and populates its finger table. 

![Behold, chord!](./images/ring.png)

### An Aside: The Introducer is Special

But wait, you ask, how does the node know where the ring exists so it can join the group in the first place? Simple! Chord-ish relies on the concept of a "introducer" node that the new or rejoining nodes can rely on existing at a fixed address, which they can request group information from. 

What happens if the machine running the introducer loses power, is disappeared by some powerful foreign government, or just gets busy with life and doesn't respond to its messages anymore, as all things might inevitably do in distributed systems?

- Communication within the group, by Chord's nature, proceeds as normally
- New nodes or rejoining nodes continuously ping the introducer node until they get a response.

### Heartbeating

So now our node is all settled in and has its membership layer. 

1. How did I implement Chord? #TODO: make more chronological? ordering weird
   1. The Introducer is Special
   2. Hashing onto the ring
   3. Messaging method (UDP packets) with comma delimited fields
   4. Fingertables
   5. Heartbeating
   6. Failure Detection
   7. Suspicion Mechanism 
2. Why'd I use Docker? be brief
   1. See Docker presentation (expand on further in future overview post)
3. Why was it a pain in the ass?
   1. Getting Docker containers to talk to each other (expand on further in future overview post)
   2. Debugging an asynchronous system (expand on further in future overview post)
4. Final result & featureset
   1. API, how to interface with some

## Setup

1. `docker-compose build && docker-compose up --scale worker=<num-workers>`
   1. For `num-workers`, it's stable for 3 - 5 workers. You can scale to as many nodes as you want though; if you don't mind Docker eating all your CPU.

