// Command olcwrt is the OpenWrt companion of the olcRTC client.
//
// It supervises the olcrtc binary, redirects LAN TCP traffic into its SOCKS5
// listener, resolves DNS through the tunnel, manages servers, subscriptions
// and domain lists stored in UCI, and serves the LuCI rpcd plugin.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

// Runtime locations. They can be overridden through the environment, which
// is used by tests and when running outside of OpenWrt.
var (
	runDir     = envOr("OLCWRT_RUNDIR", "/var/run/olcwrt")
	dataDir    = envOr("OLCWRT_DATADIR", "/etc/olcwrt")
	logPath    = envOr("OLCWRT_LOG", "/var/log/olcwrt.log")
	olcrtcPath = envOr("OLCWRT_OLCRTC", "/usr/bin/olcrtc")
	uciPkg     = envOr("OLCWRT_UCI", "olcwrt")
)

func statusPath() string  { return filepath.Join(runDir, "status.json") }
func pidPath() string     { return filepath.Join(runDir, "olcwrt.pid") }
func yamlPath() string    { return filepath.Join(runDir, "olcrtc.yaml") }
func jobsDir() string     { return filepath.Join(runDir, "jobs") }
func lockPath() string    { return filepath.Join(runDir, "olcwrt.lock") }
func runLockPath() string { return filepath.Join(runDir, "daemon.lock") }
func subsDir() string     { return filepath.Join(dataDir, "subs") }
func listsDir() string    { return filepath.Join(dataDir, "lists") }
func userListPath(kind string) string {
	return filepath.Join(dataDir, kind+".lst")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

const usageText = `usage: olcwrt <command> [args]

commands:
  run                     run the daemon (started by /etc/init.d/olcwrt)
  reload                  ask the running daemon to reload its configuration
  cleanup                 remove firewall and dnsmasq integration
  status                  print daemon status as JSON
  import <text|url|file>  import olcrtc:// links or add a subscription URL ("-" reads stdin)
  sub update [id...]      update subscriptions (all enabled when no id given)
  lists update            download remote domain/IP lists
  check [server-id]       check the running tunnel, or test a server in a temporary instance
  genconf                 print the generated olcrtc YAML configuration
  export [server-id...]   print servers as olcrtc:// links
  version                 print version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	var err error
	switch cmd {
	case "run":
		err = cmdRun()
	case "reload":
		err = cmdReload()
	case "cleanup":
		err = cmdCleanup()
	case "status":
		err = cmdStatus()
	case "import":
		err = cmdImport(args)
	case "sub", "subscription":
		err = cmdSub(args)
	case "lists":
		err = cmdLists(args)
	case "check":
		err = cmdCheck(args)
	case "genconf":
		err = cmdGenconf()
	case "export":
		err = cmdExport(args)
	case "rpcd":
		err = cmdRpcd(args)
	case "job":
		err = cmdJob(args)
	case "version", "-v", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usageText)
	default:
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "olcwrt:", err)
		os.Exit(1)
	}
}
