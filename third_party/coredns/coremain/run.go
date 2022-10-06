// This file is a subset https://github.com/coredns/coredns/blob/v010/coremain/run.go
//
// The following changes have been applied compare to the original code:
// - remove code related to command line flags
// - hard-code caddy file content
// - only link plugins needed by KCP

package coremain

import (
	"log"
	"os"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	_ "github.com/coredns/coredns/plugin/errors"
	_ "github.com/coredns/coredns/plugin/forward"
	_ "github.com/coredns/coredns/plugin/whoami"

	_ "github.com/kcp-dev/kcp/pkg/dns/plugin/nsmap"
)

const (
	// Hard-coded kcp DNS configuration
	conf = `.:5353 {
      errors
      nsmap
      forward . /etc/resolv.conf
}`
)

func init() {
	// Includes nsmap to the list of directive. Only includes the directives used in the hard-coded configuration
	dnsserver.Directives = []string{
		"errors",
		"nsmap",
		"forward",
	}
}

// Start kcp DNS server
func Start() {
	caddy.TrapSignals()

	log.SetOutput(os.Stdout)
	log.SetFlags(0) // Set to 0 because we're doing our own time, with timezone

	corefile := caddy.CaddyfileInput{
		Contents:       []byte(conf),
		Filepath:       caddy.DefaultConfigFile,
		ServerTypeName: "dns",
	}

	// Start your engines
	instance, err := caddy.Start(corefile)
	if err != nil {
		mustLogFatal(err)
	}

	// Twiddle your thumbs
	instance.Wait()
}

// mustLogFatal wraps log.Fatal() in a way that ensures the
// output is always printed to stderr so the user can see it
// if the user is still there, even if the process log was not
// enabled. If this process is an upgrade, however, and the user
// might not be there anymore, this just logs to the process
// log and exits.
func mustLogFatal(args ...interface{}) {
	if !caddy.IsUpgrade() {
		log.SetOutput(os.Stderr)
	}
	log.Fatal(args...)
}
