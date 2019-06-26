package gurnel

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/mikeraimondi/journalentry/v2"
)

//go:generate go run ../../scripts/generate_ref.go
var refFreqs map[string]float64 // populated by generated code

type statsCmd struct{}

func (*statsCmd) Name() string       { return "stats" }
func (*statsCmd) ShortHelp() string  { return "View journal statistics" }
func (*statsCmd) LongHelp() string   { return "TODO" }
func (*statsCmd) Flag() flag.FlagSet { return flag.FlagSet{} }

func (*statsCmd) Run(conf *config, args []string) error {
	wd, err := os.Getwd()
	if err != nil {
		return errors.New("getting working directory " + err.Error())
	}
	wd, err = filepath.EvalSymlinks(wd)
	if err != nil {
		return errors.New("evaluating symlinks " + err.Error())
	}

	done := make(chan struct{})
	defer close(done)
	paths, errc := walkFiles(done, wd)
	c := make(chan result)
	var wg sync.WaitGroup
	const numScanners = 32
	wg.Add(numScanners)
	for i := 0; i < numScanners; i++ {
		go func() {
			entryScanner(done, paths, c)
			wg.Done()
		}()
	}
	go func() {
		wg.Wait()
		close(c)
	}()
	var entryCount float64
	wordMap := make(map[string]uint64)
	t := time.Now()
	minDate := t
	for r := range c {
		if r.err != nil {
			return r.err
		}
		entryCount++
		for word, count := range r.wordMap {
			wordMap[word] += count
		}
		if minDate.After(r.date) {
			minDate = r.date
		}
	}
	// Check whether the Walk failed.
	if err := <-errc; err != nil {
		return err
	}
	if entryCount > 0 {
		percent := entryCount / math.Floor(t.Sub(minDate).Hours()/24)
		const outFormat = "Jan 2 2006"
		fmt.Printf("%.2f%% of days journaled since %v\n", percent*100, minDate.Format(outFormat))
		var wordCount uint64
		for _, count := range wordMap {
			wordCount += count
		}
		fmt.Printf("Total word count: %v\n", wordCount)
		avgCount := float64(wordCount) / entryCount
		fmt.Printf("Average word count: %.1f\n", avgCount)
		fmt.Print("\n")

		if len(refFreqs) == 0 {
			return nil // no code generation. exit early
		}

		wordStats := make([]*wordStat, len(wordMap))
		i := 0
		for word, count := range wordMap {
			frequency := float64(count) / float64(wordCount)
			var relFrequency float64
			refFrequency := refFreqs[word]
			if frequency > refFrequency {
				if refFrequency > 0 {
					relFrequency = frequency / refFrequency
				}
			} else {
				relFrequency = (refFrequency / frequency) * -1
			}
			wordStats[i] = &wordStat{word: word, occurrences: count, frequency: relFrequency}
			i++
		}

		sort.Slice(wordStats, func(i, j int) bool {
			return wordStats[i].frequency > wordStats[j].frequency
		})

		topUnusualWordCount := 100
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		fmt.Printf("Top %v unusually frequent words:\n", topUnusualWordCount)
		for _, ws := range wordStats[:topUnusualWordCount] {
			fmt.Fprintf(w, "%v\t%.1fX\n", ws.word, ws.frequency)
		}
		w.Flush()
		fmt.Print("\n")
		fmt.Printf("Top %v unusually infrequent words:\n", topUnusualWordCount)
		for i := 1; i <= topUnusualWordCount; i++ {
			ws := wordStats[len(wordStats)-i]
			fmt.Fprintf(w, "%v\t%.1fX\n", ws.word, ws.frequency)
		}
		w.Flush()
	}
	return nil
}

type result struct {
	wordMap map[string]uint64
	date    time.Time
	err     error
}

type wordStat struct {
	word        string
	occurrences uint64
	frequency   float64
}

func walkFiles(done <-chan struct{}, root string) (<-chan string, <-chan error) {
	paths := make(chan string)
	errc := make(chan error, 1)
	visited := make(map[string]bool)
	go func() {
		// Close the paths channel after Walk returns.
		defer close(paths)
		// No select needed for this send, since errc is buffered.
		errc <- filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || visited[info.Name()] || !journalentry.IsEntry(path) {
				return nil
			}
			visited[info.Name()] = true
			select {
			case paths <- path:
			case <-done:
				return errors.New("walk canceled")
			}
			return nil
		})
	}()
	return paths, errc
}

func entryScanner(done <-chan struct{}, paths <-chan string, c chan<- result) {
	for path := range paths {
		p := &journalentry.Entry{Path: path}
		m := make(map[string]uint64)
		_, err := p.Load()
		if err == nil {
			for _, word := range p.Words() {
				m[strings.ToLower(string(word))]++
			}
		}
		date, _ := p.Date()
		select {
		case c <- result{date: date, wordMap: m, err: err}:
		case <-done:
			return
		}
	}
}