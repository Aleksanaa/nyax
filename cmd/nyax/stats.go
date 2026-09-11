// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/metrics"
	"time"

	"github.com/alecthomas/kingpin"
)

// --stats says where a run went. Wall time on its own cannot tell an
// evaluator that allocates too much from one that computes too much, and the
// two want opposite fixes, so this reports both: the phases of the run, and
// what the runtime spent underneath them.
var stats = kingpin.Flag("stats",
	"Report where the time went: phases, allocation, and the collector's share.").Bool()

// statNames are the runtime's own accounts. The CPU classes add up to the
// whole run across all threads, so the collector's share is exact rather than
// inferred from a profile's samples.
var statNames = []string{
	"/cpu/classes/user:cpu-seconds",
	"/cpu/classes/gc/mark/assist:cpu-seconds",
	"/cpu/classes/gc/mark/dedicated:cpu-seconds",
	"/cpu/classes/gc/mark/idle:cpu-seconds",
	"/cpu/classes/gc/pause:cpu-seconds",
	"/gc/heap/allocs:bytes",
	"/gc/heap/allocs:objects",
	"/gc/heap/live:bytes",
	"/gc/cycles/total:gc-cycles",
}

// phase is one stage of a run, timed when --stats is on.
type phase struct {
	name string
	took time.Duration
}

var (
	phases    []phase
	statStart = time.Now()
)

// timed runs f, recording how long it took as a phase of the run.
func timed[T any](name string, f func() T) T {
	if !*stats {
		return f()
	}
	start := time.Now()
	result := f()
	phases = append(phases, phase{name, time.Since(start)})
	return result
}

// report prints the breakdown, which main does last.
func report() {
	if !*stats {
		return
	}
	wall := time.Since(statStart)
	samples := make([]metrics.Sample, len(statNames))
	for i, name := range statNames {
		samples[i].Name = name
	}
	metrics.Read(samples)
	read := func(name string) metrics.Sample {
		for _, s := range samples {
			if s.Name == name {
				return s
			}
		}
		return metrics.Sample{}
	}
	seconds := func(name string) float64 { return read(name).Value.Float64() }

	gc := seconds("/cpu/classes/gc/mark/assist:cpu-seconds") +
		seconds("/cpu/classes/gc/mark/dedicated:cpu-seconds") +
		seconds("/cpu/classes/gc/mark/idle:cpu-seconds") +
		seconds("/cpu/classes/gc/pause:cpu-seconds")
	user := seconds("/cpu/classes/user:cpu-seconds")
	objects := read("/gc/heap/allocs:objects").Value.Uint64()
	bytes := read("/gc/heap/allocs:bytes").Value.Uint64()

	out := os.Stderr
	fmt.Fprintf(out, "\nwall %8.1f ms\n", ms(wall))
	for _, p := range phases {
		fmt.Fprintf(out, "  %-12s %6.1f ms\n", p.name, ms(p.took))
	}
	fmt.Fprintf(out, "cpu  %8.1f ms mutator, %.1f ms collector (%.0f%% of %d threads)\n",
		user*1e3, gc*1e3, 100*gc/max(user+gc, 1e-9), runtime.GOMAXPROCS(0))
	fmt.Fprintf(out, "heap %8d objects, %d MB allocated in %d collections, %d MB live\n",
		objects, bytes>>20, read("/gc/cycles/total:gc-cycles").Value.Uint64(),
		read("/gc/heap/live:bytes").Value.Uint64()>>20)
	// Nanoseconds per object is the number to watch when the work is
	// allocation: it says whether a change made the evaluator allocate less
	// or merely allocate the same things more cheaply.
	if objects > 0 {
		fmt.Fprintf(out, "     %8.1f ns per allocation, over the whole run\n",
			float64(wall.Nanoseconds())/float64(objects))
	}
}

func ms(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }

// timed2 is timed for the usual shape of a step that can fail.
func timed2[T any](name string, f func() (T, error)) (T, error) {
	type result struct {
		val T
		err error
	}
	r := timed(name, func() result {
		val, err := f()
		return result{val, err}
	})
	return r.val, r.err
}
