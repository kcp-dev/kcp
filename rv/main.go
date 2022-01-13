package main

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer func() {
		cancel()
	}()

	fmt.Println(`setting up "resource versions" for our "shards"`)
	const numShards = 10
	shards := make([]*int64, numShards)
	shards[0] = func() *int64 {
		v := int64(0)
		return &v
	}()
	for s := 1; s < numShards; s++ {
		shards[s] = func() *int64 {
			v := int64(rand.Intn(250) + rand.Intn(250))
			return &v
		}()
	}
	for s := 0; s < numShards; s++ {
		fmt.Printf("shards[%d]=%d\n", s, *shards[s])
	}
	var largest []int64
	offsets := map[int]int64{
		0: 0,
	}

	fmt.Println("generating synthetic write load")
	writeWg := sync.WaitGroup{}
	for i := range shards {
		writeWg.Add(1)
		go func(i int) {
			defer writeWg.Done()
			ticker := time.Tick(1 * time.Microsecond)
			cutoff := rand.Float64()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker:
					if rand.Float64() > cutoff {
						atomic.AddInt64(shards[i], 1)
					}
				}
			}
		}(i)
	}

	const numItems = 10
	items := make([]*item, numItems)
	for i := 0; i < numItems; i++ {
		items[i] = &item{history: [][]int64{}}
		for j := 0; j < len(shards); j++ {
			items[i].history = append(items[i].history, []int64{})
		}
	}
	for s := 0; s < len(shards); s++ {
		fmt.Println("generating mutations for objects")
		writeCtx, writeCancel := context.WithCancel(ctx)
		wg := sync.WaitGroup{}
		for i := 0; i < numItems; i++ {
			wg.Add(1)
			go func(s, i int) {
				defer wg.Done()
				ticker := time.Tick(1 * time.Microsecond)
				for {
					select {
					case <-writeCtx.Done():
						return
					case <-ticker:
						if rand.Float64() > 0.9 {
							rv := atomic.AddInt64(shards[s], 1)
							items[i].history[s] = append(items[i].history[s], rv)
							items[i].rvs = append(items[i].rvs, rv+offsets[s])
						}
					}
				}
			}(s, i)
		}

		time.Sleep(500 * time.Microsecond)

		if s < len(shards)-1 {
			fmt.Printf("initiating migration from %d to %d: pausing writes\n", s, s+1)
		}
		writeCancel()
		wg.Wait()

		if s < len(shards)-1 {
			fmt.Printf("finalizing migration from %d to %d: copying data\n", s, s+1)
			for i := 0; i < numItems; i++ {
				wg.Add(1)
				go func(s, i int) {
					defer wg.Done()
					rv := atomic.LoadInt64(shards[s])
					items[i].history[s] = append(items[i].history[s], rv)
				}(s+1, i)
			}
			wg.Wait()

			// figure out the largest previously-seen RV and earliest "new" RV so we can calculate offset
			largestPreviousRV := int64(0)
			smallestNewRV := int64(math.MaxInt64)
			for i := 0; i < numItems; i++ {
				numRevisions := len(items[i].rvs)
				oldestPrevious := items[i].rvs[numRevisions-1]
				if oldestPrevious > largestPreviousRV {
					largestPreviousRV = oldestPrevious
				}

				newestCurrent := items[i].history[s+1][0]
				if newestCurrent < smallestNewRV {
					smallestNewRV = newestCurrent
				}
			}

			largest = append(largest, largestPreviousRV)
			offsets[s+1] = largestPreviousRV + 1 - smallestNewRV
			fmt.Printf("offsets[%d]=%d=%d+1-%d\n", s+1, offsets[s+1], largestPreviousRV, smallestNewRV)
			for i := 0; i < numItems; i++ {
				items[i].rvs = append(items[i].rvs, items[i].history[s+1][0]+offsets[s+1])
			}
		}
	}

	cancel()
	writeWg.Wait()

	for s := 0; s < numShards; s++ {
		fmt.Printf("shards[%d]=%d\n", s, *shards[s])
		if s == 0 {
			fmt.Printf("shards[%d] over [0,%d], offset(%d)\n", s, largest[s], offsets[s])
		} else if s == numShards-1 {
			fmt.Printf("shards[%d] over [%d,inf), offset(%d)\n", s, largest[s-1], offsets[s])
		} else {
			fmt.Printf("shards[%d] over [%d,%d], offset(%d)\n", s, largest[s-1], largest[s], offsets[s])
		}
	}
	for _, item := range items {
		fmt.Printf("%#v\n", *item)
	}

	// checks:
	// - user-facing resource version is monotonically increasing
	for i := 0; i < numItems; i++ {
		for j := 0; j < len(items[i].history); j++ {
			for k := 0; k < len(items[i].history[j])-1; k++ {
				this, that := items[i].history[j][k], items[i].history[j][k+1]
				if this > that {
					fmt.Printf("items[%d].history[%d]: .[%d]=%d > .[%d]=%d\n", i, j, k, this, k+1, that)
				}
			}
		}
	}
	// - we can recover every original shard and RV from the user-facing ones and the lookup table
	for i := 0; i < numItems; i++ {
		var expected []record
		for j := 0; j < len(items[i].history); j++ {
			for k := 0; k < len(items[i].history[j]); k++ {
				expected = append(expected, record{shard: j, rv: items[i].history[j][k]})
			}
		}
		if len(expected) != len(items[i].rvs) {
			fmt.Printf("items[%d]: %d records, %d user-facing RVs\n", i, len(expected), len(items[i].rvs))
			continue
		}

		for j, rv := range items[i].rvs {
			// first, find the range it's in
			idx := len(largest) // last one is an open interval if the rest don't match
			for k := range largest {
				if rv <= largest[k] {
					idx = k
					break
				}
			}
			actual := record{shard: idx, rv: rv - offsets[idx]}
			if expected[j] != actual {
				fmt.Printf("items[%d].rvs[%d]: %d mapped to %#v, but really was %#v\n", i, j, rv, actual, expected[j])
			}
		}
	}
}

type item struct {
	history [][]int64
	rvs     []int64
}

type record struct {
	shard int
	rv int64
}
