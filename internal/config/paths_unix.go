//go:build !windows

package config

// DefaultConfigPath is where FlexWSPR looks for its configuration.
func DefaultConfigPath() string { return "/etc/flexwspr/flexwspr.yaml" }

// DefaultDataDir holds .c2 files, wsprd's files, the spot log and the
// upload queue. systemd's StateDirectory=flexwspr creates it.
func DefaultDataDir() string { return "/var/lib/flexwspr" }

// DefaultWsprd is wsprd from Debian's wsjtx package, found on PATH.
func DefaultWsprd() string { return "wsprd" }

// DefaultListen serves the web page to the whole LAN, so a headless Pi can be
// set up from another computer.
const DefaultListen = ":8080"
