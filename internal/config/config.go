// Package config loads, checks and saves the FlexWSPR configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the whole configuration file. The JSON names (used by the GUI)
// match the YAML ones.
type Config struct {
	Callsign string `yaml:"callsign" json:"callsign"`
	Grid     string `yaml:"grid" json:"grid"`

	Radio struct {
		IP     string `yaml:"ip" json:"ip"`         // blank: find the radio by discovery
		Serial string `yaml:"serial" json:"serial"` // with discovery: pick this radio
	} `yaml:"radio" json:"radio"`

	// Band is a band name ("20m") or a dial frequency in MHz ("14.0956").
	// With the schedule on, it is the band used outside every slot.
	Band  string `yaml:"band" json:"band"`
	RxAnt string `yaml:"rxant" json:"rxant"` // e.g. ANT1, RX_A; blank leaves the radio's choice
	// Antennas picks the receive antenna per band ("40m": "ANT2"); bands not
	// listed use RxAnt.
	Antennas map[string]string `yaml:"antennas" json:"antennas"`

	// CreateSlice adds a DIGU slice on the dial frequency so the band shows on
	// the radio's front panel and in other clients. Not needed for decoding.
	CreateSlice bool `yaml:"create_slice" json:"create_slice"`

	Schedule Schedule `yaml:"schedule" json:"schedule"`

	DAXIQ struct {
		Channel int  `yaml:"channel" json:"channel"` // 1-4
		Rate    int  `yaml:"rate" json:"rate"`       // 24000, 48000, 96000 or 192000
		SwapIQ  bool `yaml:"swap_iq" json:"swap_iq"` // swap I and Q (mirrors the spectrum)
	} `yaml:"daxiq" json:"daxiq"`

	Wsprd struct {
		Path string   `yaml:"path" json:"path"` // wsprd binary
		Args []string `yaml:"args" json:"args"` // extra arguments, e.g. ["-d"]
		// FlipC2 writes Q un-negated in the .c2 file. wsprd negates Q when it
		// reads a .c2; leave false unless decodes never appear.
		FlipC2 bool `yaml:"flip_c2" json:"flip_c2"`
	} `yaml:"wsprd" json:"wsprd"`

	Upload struct {
		Enabled bool   `yaml:"enabled" json:"enabled"` // send spots to wsprnet.org
		URL     string `yaml:"url" json:"url"`         // default http://wsprnet.org/post/
	} `yaml:"upload" json:"upload"`

	Web struct {
		// Listen is the address of the built-in web page, e.g. ":8080";
		// "off" turns it off.
		Listen string `yaml:"listen" json:"listen"`
	} `yaml:"web" json:"web"`

	DataDir   string `yaml:"data_dir" json:"data_dir"` // .c2 files, wsprd's hashtable, spot log
	KeepC2    bool   `yaml:"keep_c2" json:"keep_c2"`   // keep each cycle's .c2 after decoding
	SpotsFile string `yaml:"spots_file" json:"spots_file"`
	Debug     bool   `yaml:"debug" json:"debug"`

	DialHz int64 `yaml:"-" json:"-"` // resolved from Band
}

// Standard WSPR dial frequencies in Hz (USB dial; signals at +1400..1600 Hz).
var Bands = map[string]int64{
	"2200m": 136000,
	"630m":  474200,
	"160m":  1836600,
	"80m":   3568600,
	"60m":   5287200,
	"40m":   7038600,
	"30m":   10138700,
	"20m":   14095600,
	"17m":   18104600,
	"15m":   21094600,
	"12m":   24924600,
	"10m":   28124600,
	"6m":    50293000,
}

// BandOrder lists the band names from low to high, for menus.
var BandOrder = []string{"2200m", "630m", "160m", "80m", "60m", "40m", "30m", "20m", "17m", "15m", "12m", "10m", "6m"}

// Load reads and checks a configuration file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := Default()
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.Resolve(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Save writes the configuration to path (atomically), creating its folder.
func (c *Config) Save(path string) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	hdr := "# FlexWSPR configuration (written by FlexWSPR; edit here or in the GUI)\n"
	if err := os.WriteFile(tmp, append([]byte(hdr), b...), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Default returns the defaults applied before the file is read.
func Default() *Config {
	c := &Config{Band: "20m", CreateSlice: true, DataDir: DefaultDataDir()}
	c.DAXIQ.Channel = 1
	c.DAXIQ.Rate = 24000
	c.Wsprd.Path = DefaultWsprd()
	c.Web.Listen = DefaultListen
	return c
}

// Clone returns a deep copy.
func (c *Config) Clone() *Config {
	n := *c
	n.Wsprd.Args = append([]string(nil), c.Wsprd.Args...)
	n.Antennas = make(map[string]string, len(c.Antennas))
	for k, v := range c.Antennas {
		n.Antennas[k] = v
	}
	n.Schedule.Rotation.Bands = append([]string(nil), c.Schedule.Rotation.Bands...)
	n.Schedule.Slots = append([]Slot(nil), c.Schedule.Slots...)
	for i := range n.Schedule.Slots {
		n.Schedule.Slots[i].Bands = append([]string(nil), c.Schedule.Slots[i].Bands...)
	}
	return &n
}

// Ready reports whether the station details needed to run are filled in.
func (c *Config) Ready() bool { return c.Callsign != "" && len(c.Grid) >= 4 }

// Resolve tidies and checks the configuration and fills in derived values.
func (c *Config) Resolve() error {
	c.Callsign = strings.ToUpper(strings.TrimSpace(c.Callsign))
	c.Grid = strings.TrimSpace(c.Grid)
	if len(c.Grid) >= 4 {
		c.Grid = strings.ToUpper(c.Grid[:2]) + c.Grid[2:4] + strings.ToLower(c.Grid[4:])
		if !validGrid(c.Grid) {
			return fmt.Errorf("grid %q is not a Maidenhead locator like FN03 or FN03ab", c.Grid)
		}
	}
	c.Radio.IP = strings.TrimSpace(c.Radio.IP)
	c.RxAnt = strings.ToUpper(strings.TrimSpace(c.RxAnt))
	ants := map[string]string{}
	for b, a := range c.Antennas {
		b, a = strings.ToLower(strings.TrimSpace(b)), strings.ToUpper(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if _, ok := Bands[b]; !ok {
			return fmt.Errorf("antennas: %q is not a band name", b)
		}
		ants[b] = a
	}
	c.Antennas = ants
	hz, err := ParseBand(c.Band)
	if err != nil {
		return err
	}
	c.DialHz = hz
	if err := c.Schedule.Check(); err != nil {
		return err
	}
	if c.DAXIQ.Channel < 1 || c.DAXIQ.Channel > 4 {
		return fmt.Errorf("daxiq.channel must be 1-4, got %d", c.DAXIQ.Channel)
	}
	switch c.DAXIQ.Rate {
	case 24000, 48000, 96000, 192000:
	default:
		return fmt.Errorf("daxiq.rate must be 24000, 48000, 96000 or 192000, got %d", c.DAXIQ.Rate)
	}
	if c.Upload.Enabled && !c.Ready() {
		return fmt.Errorf("uploading to wsprnet needs your callsign and a 4 or 6 character grid")
	}
	if c.Upload.URL == "" {
		c.Upload.URL = "http://wsprnet.org/post/"
	}
	if c.DataDir == "" {
		c.DataDir = DefaultDataDir()
	}
	if c.Wsprd.Path == "" {
		c.Wsprd.Path = DefaultWsprd()
	}
	if c.Web.Listen == "" {
		c.Web.Listen = DefaultListen
	}
	if c.SpotsFile == "" {
		c.SpotsFile = filepath.Join(c.DataDir, "spots.txt")
	}
	return nil
}

func validGrid(g string) bool {
	if len(g) != 4 && len(g) != 6 {
		return false
	}
	in := func(b, lo, hi byte) bool { return b >= lo && b <= hi }
	ok := in(g[0], 'A', 'R') && in(g[1], 'A', 'R') && in(g[2], '0', '9') && in(g[3], '0', '9')
	if len(g) == 6 {
		ok = ok && in(g[4], 'a', 'x') && in(g[5], 'a', 'x')
	}
	return ok
}

// ParseBand turns "20m" or "14.0956" (MHz) into a dial frequency in Hz.
func ParseBand(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if hz, ok := Bands[s]; ok {
		return hz, nil
	}
	var mhz float64
	if _, err := fmt.Sscanf(s, "%f", &mhz); err != nil || mhz < 0.1 || mhz > 60 {
		return 0, fmt.Errorf("band %q is not a known band name or a dial frequency in MHz", s)
	}
	return int64(mhz*1e6 + 0.5), nil
}

// AntennaFor is the receive antenna for a dial frequency: the band's entry
// in Antennas, else RxAnt ("" leaves the radio's choice).
func (c *Config) AntennaFor(hz int64) string {
	if a := c.Antennas[BandLabel(hz)]; a != "" {
		return a
	}
	return c.RxAnt
}

// BandLabel names a dial frequency: "20m" for a standard one, else MHz.
func BandLabel(hz int64) string {
	for _, n := range BandOrder {
		if Bands[n] == hz {
			return n
		}
	}
	return fmt.Sprintf("%.4f MHz", float64(hz)/1e6)
}

// Find picks the config file: the given path, else the OS default, else
// flexwspr.yaml next to the executable if one is there. When none exists it
// returns where a new one should go.
func Find(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	def := DefaultConfigPath()
	if _, err := os.Stat(def); err == nil {
		return def
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "flexwspr.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return def
}

// LoadOrDefault loads path; a missing file gives the defaults, so a new
// install can start and be set up from the GUI.
func LoadOrDefault(path string) (*Config, bool, error) {
	c, err := Load(path)
	if err == nil {
		return c, true, nil
	}
	if os.IsNotExist(err) {
		c = Default()
		return c, false, c.Resolve()
	}
	return nil, false, err
}
