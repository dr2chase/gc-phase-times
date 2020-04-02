// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// read standard input, scanning for one of:
//
// (cd ... GOPATH=/Users/drchase/work/bent/gopath ... GOROOT=/Users/drchase/work/bent/goroots/<CONFIG>/ ... -gcflags=all=-d=ssa/all/time=1 . )
// # <PACKAGE>
// <PATH>:<line>:<column>:<tab><PHASE><tab>TIME(ns)<tab><TIME><tab><FUNC-OR-METHOD>
//
// Organize the phase timings into tuples of
//  "config"       "compilation"                     "phase"   "time"
// <CONFIG>  : <NORMALIZED-PATH>,<FUNC-OR-METHOD> : <PHASE> : <TIME>
//
// For each configuration, sort the compilations by total time (sum of time, over all phases for that configuration and compilation)
// Split the sort into bins, and then for each bin and phase, report the total time for that phase in the bin,
// divided by the sum of the median phase times (per compilation) for the bin.
// The intent is that the median is not too noisy (except it is sometimes zero for very small compilations, why?)
// and this any phase that tends to be non-linear in input size will be revealed as its cost relative to bin-median will grow.
//
func main() {
	var scanner *bufio.Scanner
	if len(os.Args) > 1 { // Simplify life for running under a debugger, also use arg as input file.
		f, err := os.Open(os.Args[1])
		check(err, "Could not open %s listed on command line", os.Args[1])
		scanner = bufio.NewScanner(f)
	} else {
		scanner = bufio.NewScanner(os.Stdin)
	}
	// out := csv.NewWriter(os.Stdout)

	cfg := "UNSET_CONFIG"
	pkg := "UNSET_PACKAGE"
	gopath := "UNSET_GOPATH"
	goroot := "UNSET_GOROOT"
	pwd := "UNSET_PWD"

	phaseIndex := newStringIndex()

	newAllPhases := func() *allPhases {
		// This next bit ensures that for almost all cases, the right number of phases is pre-allocated
		return &allPhases{phases: make([]phaseTime, phaseIndex.NextIndex(), phaseIndex.NextIndex())}
	}

	allCompilations := make(map[string]map[compilation]*allPhases)
	var compilations map[compilation]*allPhases

	// String processing to scrape phase times out of a benchmark log
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.Contains(line, "gcflags=all=-d=ssa/all/time=1"):
			pwd = extractPrefixed(line, "(cd ")
			gopath = extractPrefixed(line, "GOPATH=")
			goroot = extractPrefixed(line, "GOROOT=")
			i := strings.LastIndex(goroot, "/")
			checkNN(i, "Goroot lacks trailing configuration %s", goroot)
			cfg = intern(goroot[i+1:])
			var ok bool
			compilations, ok = allCompilations[cfg]
			if !ok {
				compilations = make(map[compilation]*allPhases)
				allCompilations[cfg] = compilations
			}

		case strings.HasPrefix(line, "# "):
			pkg = intern(strings.TrimSpace(line[2:]))

		case strings.Contains(line, "TIME(ns)"):
			fields := strings.Split(line, "\t")
			for i, s := range fields {
				fields[i] = strings.TrimSpace(s)
			}
			pathLCcolon := fields[0]
			phase := phaseIndex.Index(intern(fields[1]))
			time := fields[3]
			funcOrMethod := intern(fields[4])

			// This nonsense is to shorten and normalize names across two different benchmark runs.
			// That turned out not to be necessary, but perhaps in a future version of this fine
			// piece of code it will make sense to match compilation to compilation across configurations.
			if strings.HasPrefix(pathLCcolon, "../") {
				pwdPrefix := pwd
				for strings.HasPrefix(pathLCcolon, "../") {
					pathLCcolon = pathLCcolon[3:]
					i := strings.LastIndex(pwdPrefix, "/")
					checkNN(i, "../ removal ran out of path, originals were %s and %s", fields[0], pwd)
					pwdPrefix = pwdPrefix[:i]
				}
				pathLCcolon = pwdPrefix + "/" + pathLCcolon
			}
			if strings.HasPrefix(pathLCcolon, gopath) {
				pathLCcolon = "GOPATH/" + pathLCcolon[len(gopath)+1:]
			} else if strings.HasPrefix(pathLCcolon, goroot) {
				pathLCcolon = "GOROOT/" + pathLCcolon[len(goroot)+1:]
			}
			pathLCcolon = intern(pathLCcolon)

			c := compilation{pkg: pkg, pathLCcolon: pathLCcolon, funcOrMethod: funcOrMethod}
			t, err := strconv.ParseUint(time, 10, 64)
			check(err, "Phase time was not an integer")
			allphs := compilations[c]
			if allphs == nil {
				allphs = newAllPhases()
				compilations[c] = allphs
			}
			allphs.setTime(phase, t)
		default: // ignore
		}
	}

	for _, m := range allCompilations {
		for _, allphs := range m {
			allphs.computeMedianTime()
		}
	}

	for s, m := range allCompilations {
		// Sort compilations and bin them
		const BINS = 50

		samples := make([]*allPhases, 0, len(m))
		for _, allphs := range m {
			samples = append(samples, allphs)
		}

		sort.Slice(samples, func(i, j int) bool {
			si, sj := samples[i], samples[j]
			if si.total != sj.total {
				return si.total < sj.total
			}
			return si.median < sj.median
		})

		bins := make([]*allPhases, BINS, BINS)
		binsize := float64(len(samples)) / BINS
		binI := 0
		for a := 0.0; a < float64(len(samples)); a += binsize {
			next := a + binsize
			bin := newAllPhases()
			for i := int(a); i < int(next); i++ {
				sample := samples[i]
				bin.median += sample.median
				bin.total += sample.total
				for j, t := range sample.phases {
					bin.phases[j] += t
				}
			}
			bin.computeMedianTime() // Something very flaky -- there are many w/ median == 0
			bins[binI] = bin
			binI++
		}

		f, err := os.Create(s + ".csv")
		check(err, "Could not open file for csv output")
		csvw := csv.NewWriter(f)

		title := []string{fmt.Sprintf("%s:Binned compilation phase timing profiles, bin total of phase times / bin total of per-compilation median phase times", s)}
		for i := 0; i < int(phaseIndex.NextIndex()); i++ {
			title = append(title, phaseIndex.String(int32(i)))
		}
		title = append(title, "TOTAL (ns)")
		csvw.Write(title)

		phaseTotals := make([]phaseTime, phaseIndex.NextIndex()+1)

		binI = 0
		for a := 0.0; a < float64(len(samples)); a += binsize {
			ia := int64(a)
			next := int64(a + binsize)
			row := []string{}
			row = append(row, fmt.Sprintf("[%d,%d)", ia, next))
			b := bins[binI]
			for i := 0; i < int(phaseIndex.NextIndex()); i++ {
				row = append(row, fmt.Sprintf("%5.2f", float64(b.phases[i])/float64(b.median)))
				phaseTotals[i] += b.phases[i]
			}
			row = append(row, fmt.Sprintf("%5.2f", float64(b.total)))
			csvw.Write(row)
			binI++
		}

		row := []string{}
		row = append(row, fmt.Sprintf("PHASE TOTALS (ns)"))
		total := phaseTime(0)
		for i := 0; i < int(phaseIndex.NextIndex()); i++ {
			total += phaseTotals[i]
			row = append(row, fmt.Sprintf("%d", phaseTotals[i]))
		}
		row = append(row, fmt.Sprintf("%d", total))
		csvw.Write(row)

		csvw.Flush()
		f.Close()
	}

	//out.Flush()
	check(scanner.Err(), "Problem reading (scanning) standard input")
}

type compilation struct {
	pkg, pathLCcolon, funcOrMethod string
}

type allPhases struct {
	total, median uint64
	phases        []phaseTime
}

func (aph *allPhases) setTime(phase int32, time uint64) {
	if time == 0 {
		return
	}
	for len(aph.phases) <= int(phase) {
		aph.phases = append(aph.phases, 0)
	}
	if aph.phases[phase] != 0 {
		return
	}
	aph.phases[phase] = phaseTime(time)
	aph.total += time
}

func (aph *allPhases) medianTime() uint64 {
	if aph.median == 0 {
		aph.computeMedianTime()
	}
	return aph.median
}

func (aph *allPhases) computeMedianTime() {
	l := len(aph.phases)
	scratch := make([]phaseTime, 0, l)
	scratch = append(scratch, aph.phases...)
	sort.Slice(scratch, func(i, j int) bool {
		return scratch[i] < scratch[j]
	})
	// check median A={x,y} => (A[2/2]+A[(1/2)])/2
	// check median A={x,y,z} => (A[3/2]+A[(2/2)])/2
	aph.median = uint64(scratch[l/2]+scratch[(l-1)/2]) / 2
}

type phaseTime uint64

type stringIndex struct {
	m map[string]int32
	i []string
}

func (x *stringIndex) Index(s string) int32 {
	i, ok := x.m[s]
	if !ok {
		i = int32(len(x.i))
		x.m[s] = i
		x.i = append(x.i, s)
	}
	return i
}

func (x *stringIndex) String(i int32) string {
	return x.i[i]
}

func (x *stringIndex) NextIndex() int32 {
	return int32(len(x.i))
}

func newStringIndex() *stringIndex {
	return &stringIndex{m: make(map[string]int32)}
}

var internedStrings = make(map[string]string)

func intern(s string) string {
	if r, ok := internedStrings[s]; ok {
		return r
	}
	internedStrings[s] = s
	return s
}

// extractPrefixed ensures that line begins with prefix, and returns the space-ended word
// that immediately follows prefix.  Trailing semicolon and slash are removed, and the
// result is de-duplicated (interned).
func extractPrefixed(line, prefix string) string {
	i := strings.Index(line, prefix)
	checkNN(i, "Compile line is missing %s prefixed string, line = %s", prefix, line)
	goroot := line[i+len(prefix):]
	i = strings.Index(goroot, " ")
	goroot = goroot[:i]
	if goroot[len(goroot)-1] == ';' { // easy extension to cd case
		goroot = goroot[:len(goroot)-1]
	}
	if goroot[len(goroot)-1] == '/' {
		goroot = goroot[:len(goroot)-1]
	}
	return intern(goroot)
}

// check ensures that err is nil.
// If not, it uses the additional parameters to form a an error message before panicking.
func check(err error, messages ...interface{}) {
	if err != nil {
		if len(messages) > 0 {
			maybeFmt := messages[0]
			if fm, ok := maybeFmt.(string); ok && len(messages) > 1 {
				fmt.Printf(fm, messages[1:]...)
				fmt.Println()
			} else {
				fmt.Println(maybeFmt)
			}
		}
		panic(err)
	}
}

// checkNN ensures that i is not negative.
// If not, it uses the additional parameters to form a an error message before panicking.
func checkNN(i int, messages ...interface{}) {
	if i < 0 {
		if len(messages) > 0 {
			maybeFmt := messages[0]
			if fm, ok := maybeFmt.(string); ok && len(messages) > 1 {
				fmt.Printf(fm, messages[1:]...)
				fmt.Println()
			} else {
				fmt.Println(maybeFmt)
			}
		}
		panic("Expected non-negative")
	}
}
