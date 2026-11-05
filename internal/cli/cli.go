// Package cli holds the zbxntfy command-line definition and the job-name
// derivation rules. Both the wrapper and the discovery tool use it, so a job
// gets the same name whether it is discovered in a crontab or reported by the
// wrapper.
package cli

import (
	"errors"
	"flag"
	"io"
	"path"
	"regexp"
	"strings"
)

// Options are the parsed zbxntfy arguments.
type Options struct {
	Name        string // -n: job name (default: derived from the command)
	Notify      string // -t: value of trigger tag notify:<value> (alert routing in Zabbix actions)
	AgentConfig string // -c: agent config passed to zabbix_sender
	Hostname    string // -s: host name in Zabbix
	Server      string // -z: Zabbix server/proxy (when no agent config is used)
	Port        int    // -p: server port
	Sender      string // -sender: path to zabbix_sender
	MaxBytes    int    // -m: max bytes of output sent to Zabbix
	Quiet       bool   // -q: do not report zabbix_sender failures on stderr
	DryRun      bool   // -dry-run: print what would be sent instead of sending it
	Version     bool   // -version
	Cmd         []string
}

// DefaultMaxBytes keeps job.log below the 64 KB TEXT limit of MySQL history.
const DefaultMaxBytes = 60000

// NewFlagSet returns the flag set used by zbxntfy.
func NewFlagSet(o *Options) *flag.FlagSet {
	fs := flag.NewFlagSet("zbxntfy", flag.ContinueOnError)
	fs.StringVar(&o.Name, "n", "", "job `name` (default: derived from the command)")
	fs.StringVar(&o.Notify, "t", "", "`value` of trigger tag notify:<value> used to route alerts (default: \"default\")")
	fs.StringVar(&o.AgentConfig, "c", "", "Zabbix agent `config` for zabbix_sender (default: autodetect)")
	fs.StringVar(&o.Hostname, "s", "", "`host` name in Zabbix (default: from agent config)")
	fs.StringVar(&o.Server, "z", "", "Zabbix `server` or proxy (default: from agent config)")
	fs.IntVar(&o.Port, "p", 0, "Zabbix server `port`")
	fs.StringVar(&o.Sender, "sender", "", "`path` to zabbix_sender (default: from PATH)")
	fs.IntVar(&o.MaxBytes, "m", DefaultMaxBytes, "max `bytes` of output sent to Zabbix (the tail is kept)")
	fs.BoolVar(&o.Quiet, "q", false, "do not print zabbix_sender errors")
	fs.BoolVar(&o.DryRun, "dry-run", false, "print the values instead of sending them")
	fs.BoolVar(&o.Version, "version", false, "print version and exit")
	return fs
}

// ErrNoCommand is returned when no command follows the options.
var ErrNoCommand = errors.New("no command given")

// Parse parses zbxntfy arguments (without argv[0]). Parsing stops at the first
// non-flag argument, which starts the wrapped command.
func Parse(args []string) (*Options, error) {
	o := &Options{}
	fs := NewFlagSet(o)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	o.Cmd = fs.Args()
	if o.Version {
		return o, nil
	}
	if len(o.Cmd) == 0 {
		return nil, ErrNoCommand
	}
	if o.Name == "" {
		o.Name = DeriveName(o.Cmd)
	}
	o.Name = Sanitize(o.Name)
	return o, nil
}

// interpreters and the option that introduces inline code (-> no script name)
var interpreters = map[string]string{
	"sh": "-c", "bash": "-c", "dash": "-c", "zsh": "-c", "ksh": "-c",
	"python": "-c-m", "python2": "-c-m", "python3": "-c-m",
	"perl": "-e-E", "ruby": "-e", "node": "-e", "nodejs": "-e",
	"php": "-r", "lua": "-e", "pwsh": "-c-Command",
}

type prefixCmd struct {
	positional int    // positional args before the wrapped command
	valueOpts  string // options that take a separate value, e.g. "-n-c"
}

// prefix commands that run another command
var prefixCommands = map[string]prefixCmd{
	"nice":    {0, "-n"},
	"ionice":  {0, "-c-n-p"},
	"nohup":   {0, ""},
	"chronic": {0, ""},
	"cronic":  {0, ""},
	"setsid":  {0, ""},
	"sudo":    {0, "-u-g-C-D-h-p-r-t-U"},
	"env":     {0, "-u-C-S"},
	"exec":    {0, "-a"},
	"command": {0, ""},
	"time":    {0, "-f-o"},
	"stdbuf":  {0, "-i-o-e"},
	"timeout": {1, "-s-k"},
	"flock":   {1, "-w-E"},
	"run-one": {0, ""},
}

func optIn(list, opt string) bool {
	for _, o := range strings.Split(list, "-") {
		if o != "" && "-"+o == opt {
			return true
		}
	}
	return false
}

var envAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// DeriveName derives a job name from a command line:
//
//	/opt/scripts/zaloha.sh --db          -> zaloha
//	bash /opt/x/backup.sh                -> backup
//	nice -n 10 timeout 1h /opt/sync.py   -> sync
//	run-parts --report /etc/cron.daily   -> cron.daily
func DeriveName(cmd []string) string {
	i := 0
	for i < len(cmd) {
		w := cmd[i]
		if envAssign.MatchString(w) {
			i++
			continue
		}
		base := path.Base(w)
		if p, ok := prefixCommands[base]; ok && i+1 < len(cmd) {
			i++
			for i < len(cmd) && strings.HasPrefix(cmd[i], "-") {
				if optIn(p.valueOpts, cmd[i]) && i+1 < len(cmd) {
					i++
				}
				i++
			}
			for k := 0; k < p.positional && i < len(cmd); k++ {
				i++
			}
			continue
		}
		if inline, ok := interpreters[base]; ok {
			for j := i + 1; j < len(cmd); j++ {
				a := cmd[j]
				if optIn(inline, a) {
					break // inline code: the caller should use -n
				}
				if strings.HasPrefix(a, "-") {
					continue
				}
				return stripExt(path.Base(a))
			}
			return base
		}
		if base == "run-parts" {
			for j := len(cmd) - 1; j > i; j-- {
				if !strings.HasPrefix(cmd[j], "-") {
					return path.Base(cmd[j])
				}
			}
		}
		return stripExt(base)
	}
	return "job"
}

func stripExt(s string) string {
	if i := strings.LastIndexByte(s, '.'); i > 0 {
		return s[:i]
	}
	return s
}

var unsafe = regexp.MustCompile(`[^A-Za-z0-9_./-]+`)

// Sanitize makes a name safe for use as a Zabbix item key parameter, in
// JSONPath and in macro context: only [A-Za-z0-9_./-] are kept, so a full
// path such as /opt/scripts/backup.sh is allowed.
func Sanitize(s string) string {
	s = strings.Trim(unsafe.ReplaceAllString(s, "_"), "_")
	if s == "" {
		return "job"
	}
	return s
}
