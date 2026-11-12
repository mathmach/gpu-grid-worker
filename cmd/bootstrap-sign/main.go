package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

type cliFlags struct {
	keygen        bool
	sign          bool
	verify        bool
	force         bool
	allowLocal    bool
	keyPath       string
	keyOut        string
	gatewayURL    string
	bootstrapURLs string
	inPath        string
	outPath       string
	pubKeys       string
	version       uint64
	minVersion    uint64
	expiresHours  int
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseCLIFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if _, wErr := fmt.Fprintf(stderr, "bootstrap-sign: %v\n", err); wErr != nil {
			return 2
		}
		return 2
	}
	if err := dispatchAction(cfg, stdout); err != nil {
		if _, wErr := fmt.Fprintf(stderr, "bootstrap-sign: %v\n", err); wErr != nil {
			return 1
		}
		return 1
	}
	return 0
}

func parseCLIFlags(args []string, stderr io.Writer) (cliFlags, error) {
	var cfg cliFlags
	fs := flag.NewFlagSet("bootstrap-sign", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&cfg.keygen, "keygen", false, "Generate a new Ed25519 network bootstrap keypair JSON file")
	fs.StringVar(&cfg.keyOut, "key-out", "", "Destination path for generated keypair JSON file")
	fs.BoolVar(&cfg.force, "force", false, "Allow overwriting an existing keypair file with -keygen")
	fs.BoolVar(&cfg.sign, "sign", false, "Sign a network.json manifest with an Ed25519 keypair file")
	fs.StringVar(&cfg.keyPath, "key", "", "Path to Ed25519 keypair JSON file for -sign")
	fs.StringVar(&cfg.gatewayURL, "gateway", "", "Target Gateway base URL (https://...) for -sign")
	fs.Uint64Var(&cfg.version, "version", 0, "Monotonic manifest version (>= 1) for -sign")
	fs.StringVar(&cfg.bootstrapURLs, "bootstrap-urls", "", "Optional comma-separated discovery mirror URLs for -sign")
	fs.IntVar(&cfg.expiresHours, "expires-hours", 0, "Optional manifest expiration window in hours (> 0) for -sign")
	fs.StringVar(&cfg.outPath, "out", "", "Optional output file path for signed network.json (defaults to stdout)")
	fs.BoolVar(&cfg.verify, "verify", false, "Verify a signed network.json manifest")
	fs.StringVar(&cfg.inPath, "in", "", "Input network.json path for -verify")
	fs.StringVar(&cfg.pubKeys, "pubkeys", "", "Optional comma-separated trusted Ed25519 public key hex list for -verify")
	fs.Uint64Var(&cfg.minVersion, "min-version", 1, "Minimum accepted manifest version for -verify")
	fs.BoolVar(&cfg.allowLocal, "allow-local", false, "Allow local/loopback HTTP targets (development/testing only)")
	if err := fs.Parse(args); err != nil {
		return cliFlags{}, err
	}
	return cfg, nil
}

func dispatchAction(cfg cliFlags, stdout io.Writer) error {
	modeCount := countSelectedModes(cfg.keygen, cfg.sign, cfg.verify)
	if modeCount != 1 {
		return errors.New("select exactly one mode: -keygen, -sign, or -verify")
	}
	switch {
	case cfg.keygen:
		return runKeygen(cfg.keyOut, cfg.force, stdout)
	case cfg.sign:
		return runSign(cfg, stdout)
	default:
		return runVerify(cfg, stdout)
	}
}

func countSelectedModes(modes ...bool) int {
	total := 0
	for _, enabled := range modes {
		if enabled {
			total++
		}
	}
	return total
}
