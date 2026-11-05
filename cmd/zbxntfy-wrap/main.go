// zbxntfy-wrap puts "zbxntfy" in front of existing cron jobs, or marks jobs
// with "# zbxntfy:ignore" so Zabbix does not report them as "not monitored".
//
// Run without arguments in a terminal, it starts a wizard: for each job choose
// monitor [x], ignore [-] or leave it [ ], review the changes, confirm.
// With job names (or -all) it shows the changes, and writes them with -apply.
//
// Before writing, every affected crontab is backed up to
// /var/backups/zbxntfy/<timestamp>/. /etc/crontab and /etc/cron.d/* are
// replaced atomically; user crontabs are installed with "crontab -u <user> -"
// so cron reloads them.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/DuprTECH/Zabbix-CRON-jobs-monitor/internal/crontab"
)

var version = "dev"

const usage = `Usage: zbxntfy-wrap [options] [job ...]

Puts zbxntfy in front of cron jobs, or marks them "# zbxntfy:ignore".

  zbxntfy-wrap                          wizard: monitor / ignore, review, confirm
  zbxntfy-wrap -list                    list jobs that are not monitored
  zbxntfy-wrap backup-db rotate-logs    show the change for these jobs
  zbxntfy-wrap -apply backup-db         back up the crontab, then change it
  zbxntfy-wrap -ignore -apply cleanup   add "# zbxntfy:ignore" to the line
  zbxntfy-wrap -unignore -apply cleanup remove it again
  zbxntfy-wrap -all -exclude '^tmp' -t IT_ops -apply

Options:
`

type config struct {
	notify     string
	backupRoot string
	wrapper    string
}

// showSystem: also offer distribution jobs (-system) in -list and -all.
var showSystem bool

func main() {
	fs := flag.NewFlagSet("zbxntfy-wrap", flag.ExitOnError)
	all := fs.Bool("all", false, "all jobs that are not monitored or ignored")
	exclude := fs.String("exclude", "", "with -all: skip jobs whose name matches this `regex`")
	list := fs.Bool("list", false, "only list jobs (no wizard)")
	apply := fs.Bool("apply", false, "write the changes (crontabs are backed up first)")
	ignore := fs.Bool("ignore", false, "add \"# zbxntfy:ignore\" to the named jobs instead of wrapping them")
	unignore := fs.Bool("unignore", false, "remove \"# zbxntfy:ignore\" from the named jobs")
	fs.BoolVar(&showSystem, "system", false, "with -list/-all: include distribution jobs (cron.daily, anacron/systemd-guarded lines)")
	var c config
	fs.StringVar(&c.notify, "t", "", "add \"-t `value`\" (trigger tag notify:<value>)")
	fs.StringVar(&c.backupRoot, "backup-dir", "/var/backups/zbxntfy", "`directory` for backups")
	fs.StringVar(&c.wrapper, "wrapper", defaultWrapper(), "`path` of zbxntfy to insert")
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		fs.PrintDefaults()
	}
	_ = fs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println("zbxntfy-wrap", version)
		return
	}

	jobs, errs := crontab.Scanner{}.Scan()
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "warning:", e)
	}

	names := fs.Args()
	switch {
	case *ignore || *unignore:
		if *ignore && *unignore || len(names) == 0 {
			fmt.Fprintln(os.Stderr, "zbxntfy-wrap: use -ignore or -unignore with job names")
			os.Exit(2)
		}
		os.Exit(batchIgnore(jobs, names, *ignore, *apply, c))
	case len(names) == 0 && !*all:
		interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
		if *list || !interactive {
			os.Exit(listJobs(jobs))
		}
		os.Exit(wizard(jobs, c))
	}
	os.Exit(batch(jobs, names, *all, *exclude, *apply, c))
}

func defaultWrapper() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "zbxntfy")
		if _, err := os.Stat(p); err == nil {
			return filepath.ToSlash(p)
		}
	}
	return "zbxntfy"
}

// candidates returns the jobs offered for wrapping: not monitored, not
// ignored, and no distribution job (unless -system).
func candidates(jobs []crontab.Job) []crontab.Job {
	var out []crontab.Job
	for _, j := range jobs {
		if j.Monitored == 0 && !j.Ignored && (showSystem || !j.IsDistroJob()) {
			out = append(out, j)
		}
	}
	return out
}

func listJobs(jobs []crontab.Job) int {
	show := func(title string, pick func(j *crontab.Job) bool) {
		n := 0
		for i := range jobs {
			j := &jobs[i]
			if !pick(j) {
				continue
			}
			if n == 0 {
				fmt.Println(title)
			}
			n++
			fmt.Printf("  %-30s %s:%d\n  %-30s %s %s\n", j.Name, j.FilePath(), j.LineNo(), "", j.Schedule, j.Command)
		}
		if n > 0 {
			fmt.Println()
		}
	}
	show("Not monitored (shown in Zabbix as \"not monitored\"):", func(j *crontab.Job) bool {
		return j.Monitored == 0 && !j.Ignored && !j.IsDistroJob()
	})
	show("Ignored (\"# zbxntfy:ignore\", hidden in Zabbix):", func(j *crontab.Job) bool {
		return j.Monitored == 0 && j.Ignored
	})
	if showSystem {
		show("Distribution jobs (hidden in Zabbix):", func(j *crontab.Job) bool {
			return j.Monitored == 0 && !j.Ignored && j.IsDistroJob()
		})
	}
	fmt.Println("Wizard:            zbxntfy-wrap")
	fmt.Println("Monitor jobs:      zbxntfy-wrap -apply <job> [job ...]")
	fmt.Println("Ignore jobs:       zbxntfy-wrap -ignore -apply <job> [job ...]")
	return 0
}

func findJobs(jobs []crontab.Job, names []string) ([]crontab.Job, int) {
	byName := map[string]crontab.Job{}
	for _, j := range jobs {
		byName[j.Name] = j
	}
	var out []crontab.Job
	rc := 0
	for _, n := range names {
		j, ok := byName[n]
		if !ok {
			fmt.Fprintf(os.Stderr, "zbxntfy-wrap: no cron job named %q (run with -list to see them)\n", n)
			rc = 1
			continue
		}
		out = append(out, j)
	}
	return out, rc
}

func batch(jobs []crontab.Job, names []string, all bool, exclude string, apply bool, c config) int {
	var excl *regexp.Regexp
	if exclude != "" {
		var err error
		if excl, err = regexp.Compile(exclude); err != nil {
			fmt.Fprintln(os.Stderr, "zbxntfy-wrap: -exclude:", err)
			return 2
		}
	}
	var selected []crontab.Job
	if all {
		for _, j := range candidates(jobs) {
			if excl == nil || !excl.MatchString(j.Name) {
				selected = append(selected, j)
			}
		}
	}
	named, rc := findJobs(jobs, names)
	for _, j := range named {
		if j.Monitored == 1 {
			fmt.Printf("%s: already monitored\n", j.Name)
			continue
		}
		selected = append(selected, j)
	}

	var edits []crontab.Edit
	opts := crontab.WrapOptions{Wrapper: c.wrapper, Notify: c.notify}
	for _, j := range selected {
		if e, ok := planOne(j, func() (string, error) { return crontab.PlanWrap(j, opts) }); ok {
			edits = append(edits, e)
		} else {
			rc = 1
		}
	}
	return finish(edits, apply, rc, c)
}

func batchIgnore(jobs []crontab.Job, names []string, ignore, apply bool, c config) int {
	named, rc := findJobs(jobs, names)
	var edits []crontab.Edit
	for _, j := range named {
		if e, ok := planOne(j, func() (string, error) { return crontab.PlanIgnore(j, ignore) }); ok {
			edits = append(edits, e)
		} else {
			rc = 1
		}
	}
	return finish(edits, apply, rc, c)
}

func finish(edits []crontab.Edit, apply bool, rc int, c config) int {
	if len(edits) == 0 {
		return rc
	}
	if !apply {
		fmt.Println("\nNothing written. Add -apply to make these changes.")
		return rc
	}
	if arc := applyEdits(edits, c); arc != 0 {
		rc = arc
	}
	return rc
}

// planOne computes one change and prints it as a diff.
func planOne(j crontab.Job, plan func() (string, error)) (crontab.Edit, bool) {
	nl, err := plan()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: skipped: %v\n", j.Name, err)
		return crontab.Edit{}, false
	}
	fmt.Printf("%s:%d  %s\n  - %s\n  + %s\n", j.FilePath(), j.LineNo(), j.Name,
		crontab.MaskSecrets(strings.TrimSpace(j.RawLine())), crontab.MaskSecrets(strings.TrimSpace(nl)))
	return crontab.Edit{Job: j, NewLine: nl}, true
}

func applyEdits(edits []crontab.Edit, c config) int {
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "zbxntfy-wrap: writing crontabs requires root")
		return 1
	}
	a := crontab.Applier{
		BackupDir:          filepath.Join(c.backupRoot, time.Now().Format("20060102-150405")),
		InstallUserCrontab: installUserCrontab,
	}
	rc := 0
	fmt.Println()
	for _, r := range a.Apply(edits) {
		if r.Err != nil {
			fmt.Fprintf(os.Stderr, "FAILED  %s: %v\n", r.File, r.Err)
			if r.Backup != "" {
				fmt.Fprintf(os.Stderr, "        backup: %s\n", r.Backup)
			}
			rc = 1
			continue
		}
		fmt.Printf("changed %s (%d line(s))\n", r.File, r.Edits)
		fmt.Printf("        backup:  %s\n", r.Backup)
		fmt.Printf("        restore: %s\n", r.Restore)
	}
	fmt.Println("\nZabbix sees the change within a minute (discovery runs every minute).")
	return rc
}

func installUserCrontab(user, content string) error {
	cmd := exec.Command("crontab", "-u", user, "-")
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("crontab -u %s: %v: %s", user, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// wizard states
const (
	stNone    = 0 // leave as it is: Zabbix shows "not monitored"
	stMonitor = 1 // wrap with zbxntfy
	stIgnore  = 2 // "# zbxntfy:ignore" (distribution jobs are hidden anyway)
)

type item struct {
	job    crontab.Job
	distro bool
	state  int
}

// wizard: choose monitor / ignore per job, choose a notification tag,
// review, confirm, apply.
func wizard(jobs []crontab.Job, c config) int {
	var items []item
	monitored := 0
	// regular jobs first, then ignored, then distribution jobs
	for pass := 0; pass < 3; pass++ {
		for _, j := range jobs {
			if j.Monitored == 1 {
				if pass == 0 {
					monitored++
				}
				continue
			}
			d := j.IsDistroJob()
			p := 0
			switch {
			case d:
				p = 2
			case j.Ignored:
				p = 1
			}
			if p != pass {
				continue
			}
			st := stNone
			if j.Ignored || d {
				st = stIgnore
			}
			items = append(items, item{job: j, distro: d, state: st})
		}
	}
	if len(items) == 0 {
		fmt.Println("All cron jobs are monitored by zbxntfy.")
		return 0
	}

	if !pick(items, monitored) {
		fmt.Println("Cancelled, nothing changed.")
		return 0
	}

	in := bufio.NewReader(os.Stdin)
	wraps := 0
	for _, it := range items {
		if it.state == stMonitor {
			wraps++
		}
	}
	if wraps > 0 && c.notify == "" {
		fmt.Print("Notification tag for alerts (zbxntfy -t <value>), Enter = default: ")
		line, _ := in.ReadString('\n')
		c.notify = strings.TrimSpace(line)
	}

	fmt.Println()
	opts := crontab.WrapOptions{Wrapper: c.wrapper, Notify: c.notify}
	var edits []crontab.Edit
	for _, it := range items {
		j := it.job
		var plan func() (string, error)
		switch {
		case it.state == stMonitor:
			plan = func() (string, error) { return crontab.PlanWrap(j, opts) }
		case it.state == stIgnore && !j.Ignored && !it.distro:
			plan = func() (string, error) { return crontab.PlanIgnore(j, true) }
		case it.state != stIgnore && j.Ignored:
			plan = func() (string, error) { return crontab.PlanIgnore(j, false) }
		default:
			continue // no change
		}
		if e, ok := planOne(j, plan); ok {
			edits = append(edits, e)
		}
	}
	if len(edits) == 0 {
		fmt.Println("No changes.")
		return 0
	}
	fmt.Printf("\nApply %d change(s)? The crontabs are backed up first. [y/N]: ", len(edits))
	line, _ := in.ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" && a != "a" && a != "ano" {
		fmt.Println("Nothing changed.")
		return 0
	}
	return applyEdits(edits, c)
}

// pick shows the job list. It returns false when the user cancelled.
func pick(items []item, monitored int) bool {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zbxntfy-wrap: cannot use the terminal:", err)
		return false
	}
	out := os.Stdout
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l") // alternate screen, hide cursor
	defer func() {
		fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
		_ = term.Restore(fd, old)
	}()

	cur, top := 0, 0
	buf := make([]byte, 16)
	for {
		w, h, err := term.GetSize(int(out.Fd()))
		if err != nil || w < 40 || h < 10 {
			w, h = 100, 30
		}
		rows := h - 7
		if cur < top {
			top = cur
		}
		if cur >= top+rows {
			top = cur - rows + 1
		}

		var b strings.Builder
		b.WriteString("\x1b[H\x1b[2J")
		b.WriteString("\x1b[1mCron jobs: choose what to monitor with zbxntfy\x1b[0m")
		if monitored > 0 {
			fmt.Fprintf(&b, "  (%d already monitored)", monitored)
		}
		b.WriteString("\r\n\x1b[2m[x] monitor   [-] ignore (hidden in Zabbix)   [ ] leave as is (\"not monitored\" in Zabbix)\x1b[0m\r\n")
		b.WriteString("\x1b[2m↑/↓ move   space monitor   i ignore   a all   n none   enter continue   q quit\x1b[0m\r\n\r\n")
		for i := top; i < len(items) && i < top+rows; i++ {
			it := items[i]
			j := it.job
			box := "[ ]"
			switch it.state {
			case stMonitor:
				box = "[\x1b[32mx\x1b[0m]"
			case stIgnore:
				box = "[\x1b[33m-\x1b[0m]"
			}
			name := j.Name
			if it.distro {
				name += " (system)"
			}
			text := fmt.Sprintf("%-28s %-15s %s", trunc(name, 28), trunc(j.Schedule, 15), j.Command)
			line := fmt.Sprintf(" %s %s", box, trunc(text, w-7))
			switch {
			case i == cur:
				line = "\x1b[7m>" + line + "\x1b[0m"
			case it.distro:
				line = " \x1b[2m" + line + "\x1b[0m"
			default:
				line = " " + line
			}
			b.WriteString(line + "\r\n")
		}
		nm, ni := 0, 0
		for _, it := range items {
			switch it.state {
			case stMonitor:
				nm++
			case stIgnore:
				ni++
			}
		}
		fmt.Fprintf(&b, "\r\nmonitor %d, ignore %d, of %d   %s (%s)", nm, ni, len(items),
			trunc(items[cur].job.FilePath(), w-40), items[cur].job.User)
		fmt.Fprint(out, b.String())

		k, err := os.Stdin.Read(buf)
		if err != nil {
			return false
		}
		switch key := string(buf[:k]); key {
		case "\x1b[A", "k":
			if cur > 0 {
				cur--
			}
		case "\x1b[B", "j":
			if cur < len(items)-1 {
				cur++
			}
		case "\x1b[5~": // page up
			cur = max(0, cur-rows)
		case "\x1b[6~": // page down
			cur = min(len(items)-1, cur+rows)
		case " ", "x":
			if items[cur].state == stMonitor {
				items[cur].state = stNone
			} else {
				items[cur].state = stMonitor
			}
			if cur < len(items)-1 {
				cur++
			}
		case "i", "-":
			if items[cur].state == stIgnore {
				items[cur].state = stNone
			} else {
				items[cur].state = stIgnore
			}
			if cur < len(items)-1 {
				cur++
			}
		case "a":
			for i := range items {
				if items[i].state == stNone {
					items[i].state = stMonitor
				}
			}
		case "n":
			for i := range items {
				if items[i].state == stMonitor {
					items[i].state = stNone
				}
			}
		case "\r", "\n":
			return true
		case "q", "\x03", "\x1b":
			return false
		}
	}
}

func trunc(s string, n int) string {
	r := []rune(s)
	if n <= 1 || len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
