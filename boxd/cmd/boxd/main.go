// boxd is the Syncphony box's helper daemon (MAD-803, ADR 0005 in the box
// repo; the browser-facing bridge contract is Syncphony ADR 0016). It owns
// everything on the box that isn't Chromium: the syncphony.txt config, the
// box bridge on loopback, and the local setup and offline screens.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/config"
	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/daemon"
)

// version is the box software's version, stamped in by the build
// (git describe); the kiosk passes it to /tv?box=<version>.
var version = "dev"

func main() {
	log.SetPrefix("boxd: ")
	log.SetFlags(log.LstdFlags)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve":
			serve(os.Args[2:])
		case "config":
			configCmd(os.Args[2:])
		case "parse", "apply": // the stage-1 spellings, kept working
			configCmd(os.Args[1:])
		case "url":
			urlCmd()
		case "version", "--version", "-v":
			fmt.Println(version)
		case "help", "--help", "-h":
			usage()
		default:
			fmt.Fprintf(os.Stderr, "boxd: unknown command %q\n\n", os.Args[1])
			usage()
			os.Exit(2)
		}
		return
	}
	serve(nil)
}

func usage() {
	fmt.Fprint(os.Stderr, `boxd — the Syncphony box helper daemon

usage:
  boxd [serve]          run the daemon (box bridge + local screens)
  boxd config parse [FILE]   print the validated config.env content
  boxd config apply      write config.env, set the hostname, sync cmdline
  boxd parse|apply       the same, stage-1 spellings
  boxd url               print the /tv URL the kiosk opens (if configured)
  boxd --version         print the box software version
`)
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	fs.Parse(args)
	d := daemon.New(version, log.Default(), config.RealRunCommand)
	if err := d.Run(); err != nil {
		log.Fatalf("boxd: %v", err)
	}
}

func configCmd(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "parse":
		path := ""
		if len(args) > 1 {
			path = args[1]
		} else if p := os.Getenv("SB_TXT"); p != "" {
			path = p
		} else {
			path = config.DefaultTxtPath
		}
		cfg := config.ParseFile(path)
		for _, w := range cfg.Warnings {
			fmt.Fprintf(os.Stderr, "boxd config: %s\n", w)
		}
		fmt.Print(cfg.EnvContent())
	case "apply":
		config.Apply(config.PathsFromEnv(), config.RealRunCommand, log.Default())
	default:
		fmt.Fprintf(os.Stderr, "boxd: unknown config command %q\n", args[0])
		os.Exit(2)
	}
}

// urlCmd prints the /tv target for the kiosk wrapper (used when the daemon
// is down and the wrapper must navigate straight to the server). Prints
// nothing and exits 1 when no server is configured.
func urlCmd() {
	cfg := config.ParseFile(config.PathsFromEnv().Txt)
	if cfg.ServerURL == "" {
		os.Exit(1)
	}
	fmt.Println(config.TargetURL(cfg.ServerURL, version, cfg.Name))
}
