package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/ffuf/ffuf/v2/pkg/assembly"
	"github.com/ffuf/ffuf/v2/pkg/engine"
	"github.com/ffuf/ffuf/v2/pkg/ffuf"
	"github.com/ffuf/ffuf/v2/pkg/filter"
	"github.com/ffuf/ffuf/v2/pkg/input"
	"github.com/ffuf/ffuf/v2/pkg/interactive"
	"github.com/ffuf/ffuf/v2/pkg/runner"
)

// flagRegistry describes the registered flags for the segmented help output
// (see Usage in help.go). It is (re)populated on every ParseFlags call.
var flagRegistry *ffuf.FlagRegistry

// ParseFlags registers every CLI flag from ffuf.RegisterFlags — the single source
// of truth — onto the global flag set and parses the command line into opts.
// Flag defaults are the current field values, so a value loaded from a config
// file is used unless the same flag is given on the command line (file < CLI).
func ParseFlags(opts *ffuf.ConfigOptions) *ffuf.ConfigOptions {
	flagRegistry = ffuf.RegisterFlags(flag.CommandLine, opts)
	flag.Usage = Usage
	flag.Parse()
	return opts
}

func main() {

	var err, optserr error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// prepare the default config options from default config file
	var opts *ffuf.ConfigOptions
	opts, optserr = ffuf.ReadDefaultConfig()

	opts = ParseFlags(opts)

	// Handle searchhash functionality and exit
	if opts.General.Searchhash != "" {
		coptions, pos, err := engine.SearchHash(opts.General.Searchhash)
		if err != nil {
			fmt.Printf("[ERR] %s\n", err)
			os.Exit(1)
		}
		if len(coptions) > 0 {
			fmt.Printf("Request candidate(s) for hash %s\n", opts.General.Searchhash)
		}
		for _, copt := range coptions {
			conf, err := ffuf.ConfigFromOptions(&copt.ConfigOptions, ctx, cancel)
			if err != nil {
				continue
			}
			ok, reason := engine.HistoryReplayable(conf)
			if ok {
				printSearchResults(conf, pos, copt.Time, opts.General.Searchhash)
			} else {
				fmt.Printf("[ERR] Hash cannot be mapped back because %s\n", reason)
			}

		}
		if err != nil {
			fmt.Printf("[ERR] %s\n", err)
		}
		os.Exit(0)
	}

	if opts.General.ShowVersion {
		fmt.Printf("ffuf version: %s (custom build)\n", ffuf.Version())
		os.Exit(0)
	}

	if opts.General.SaveDefaults {
		targetFile := ffuf.GetDefaultConfigFile()
		currentConf, _ := ffuf.ReadConfig(targetFile)
		if currentConf == nil {
			currentConf = ffuf.NewConfigOptions()
		}

		savedItems := []string{}
		flag.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "w":
				currentConf.Input.Wordlists = opts.Input.Wordlists
				savedItems = append(savedItems, fmt.Sprintf("wordlist: %s", strings.Join(opts.Input.Wordlists, ", ")))
			case "t":
				currentConf.General.Threads = opts.General.Threads
				savedItems = append(savedItems, fmt.Sprintf("threads: %d", opts.General.Threads))
			case "u":
				currentConf.HTTP.URL = opts.HTTP.URL
				savedItems = append(savedItems, fmt.Sprintf("url: %s", opts.HTTP.URL))
			case "timeout":
				currentConf.HTTP.Timeout = opts.HTTP.Timeout
				savedItems = append(savedItems, fmt.Sprintf("timeout: %d", opts.HTTP.Timeout))
			case "p":
				currentConf.General.Delay = opts.General.Delay
				savedItems = append(savedItems, fmt.Sprintf("delay: %s", opts.General.Delay))
			case "rate":
				currentConf.General.Rate = opts.General.Rate
				savedItems = append(savedItems, fmt.Sprintf("rate: %d", opts.General.Rate))
			}
		})

		if len(savedItems) == 0 {
			if len(opts.Input.Wordlists) > 0 {
				currentConf.Input.Wordlists = opts.Input.Wordlists
				savedItems = append(savedItems, fmt.Sprintf("wordlist: %s", strings.Join(opts.Input.Wordlists, ", ")))
			}
			if opts.General.Threads > 0 {
				currentConf.General.Threads = opts.General.Threads
				savedItems = append(savedItems, fmt.Sprintf("threads: %d", opts.General.Threads))
			}
		}

		if err := ffuf.WriteConfig(targetFile, currentConf); err != nil {
			fmt.Fprintf(os.Stderr, "[ERR] Could not save configuration to %s: %s\n", targetFile, err)
			os.Exit(1)
		}

		fmt.Printf("[+] Successfully saved defaults to %s:\n", targetFile)
		for _, item := range savedItems {
			fmt.Printf("    • %s\n", item)
		}
		os.Exit(0)
	}
	if len(opts.Output.DebugLog) != 0 {
		f, err := os.OpenFile(opts.Output.DebugLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Disabling logging, encountered error(s): %s\n", err)
			log.SetOutput(io.Discard)
		} else {
			log.SetOutput(f)
			defer f.Close()
		}
	} else {
		log.SetOutput(io.Discard)
	}
	if optserr != nil {
		log.Printf("Error while opening default config file: %s", optserr)
	}

	if opts.General.ConfigFile != "" {
		opts, err = ffuf.ReadConfig(opts.General.ConfigFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Encoutered error(s): %s\n", err)
			Usage()
			fmt.Fprintf(os.Stderr, "Encoutered error(s): %s\n", err)
			os.Exit(1)
		}
		// Reset the flag package state
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
		// Re-parse the cli options
		opts = ParseFlags(opts)
	}

	// Set up Config struct
	conf, err := ffuf.ConfigFromOptions(opts, ctx, cancel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Encountered error(s): %s\n", err)
		Usage()
		fmt.Fprintf(os.Stderr, "Encountered error(s): %s\n", err)
		os.Exit(1)
	}

	job, err := assembly.BuildJob(conf)

	if job.AuditLogger != nil {
		defer job.AuditLogger.Close()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Encountered error(s): %s\n", err)
		Usage()
		fmt.Fprintf(os.Stderr, "Encountered error(s): %s\n", err)
		os.Exit(1)
	}
	if err := SetupFilters(opts, conf); err != nil {
		fmt.Fprintf(os.Stderr, "Encountered error(s): %s\n", err)
		Usage()
		fmt.Fprintf(os.Stderr, "Encountered error(s): %s\n", err)
		os.Exit(1)
	}

	if !conf.Noninteractive {
		go func() {
			err := interactive.Handle(job)
			if err != nil {
				log.Printf("Error while trying to initialize interactive session: %s", err)
			}
		}()
	}

	// Job handles waiting for goroutines to complete itself
	job.Start()
}

// SetupFilters installs the matchers and filters onto conf.MatcherManager. The
// matcher/filter construction itself lives in filter.FromConfig (pure, testable);
// this shim owns only the one decision that genuinely needs the global CLI flag
// set - whether to install the default status matcher - plus the -ignore-body
// warning.
func SetupFilters(parseOpts *ffuf.ConfigOptions, conf *ffuf.Config) error {
	// The default status matcher is installed unless another matcher was explicitly
	// passed on the CLI. Only the global flag set knows which flags were passed vs
	// defaulted, so this decision cannot move into the library.
	statusSet := false
	matcherSet := false
	// responseMatcherSet tracks the response-body matchers (ms/ml/mw) explicitly
	// passed on the CLI. The -ignore-body warning keys off CLI-passed flags (as the
	// original did via flag.Visit), NOT off the config value, so a config-file /
	// .ffufrc value does not spuriously trigger it.
	responseMatcherSet := false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "mc":
			statusSet = true
		case "ms", "ml", "mw":
			matcherSet = true
			responseMatcherSet = true
		case "mr", "mt":
			matcherSet = true
		}
	})

	mm, err := filter.FromConfig(parseOpts, statusSet || !matcherSet)
	conf.MatcherManager = mm

	warningIgnoreBody := responseMatcherSet ||
		parseOpts.Filter.Size != "" || parseOpts.Filter.Lines != "" || parseOpts.Filter.Words != ""
	if conf.IgnoreBody && warningIgnoreBody {
		fmt.Printf("*** Warning: possible undesired combination of -ignore-body and the response options: fl,fs,fw,ml,ms and mw.\n")
	}
	return err
}

func printSearchResults(conf *ffuf.Config, pos int, exectime time.Time, hash string) {
	inp, err := input.NewInputProvider(conf)
	if err.ErrorOrNil() != nil {
		fmt.Printf("-------------------------------------------\n")
		fmt.Println("Encountered error that prevents reproduction of the request:")
		fmt.Println(err.ErrorOrNil())
		return
	}
	inp.SetPosition(pos)
	inputdata := inp.Value()
	inputdata["FFUFHASH"] = []byte(hash)
	basereq := ffuf.BaseRequest(conf)
	dummyrunner := runner.NewSimpleRunner(conf, false)
	ffufreq, _ := dummyrunner.Prepare(inputdata, &basereq)
	rawreq, _ := dummyrunner.Dump(&ffufreq)
	fmt.Printf("-------------------------------------------\n")
	fmt.Printf("ffuf job started at: %s\n\n", exectime.Format(time.RFC3339))
	fmt.Printf("%s\n", string(rawreq))
}
