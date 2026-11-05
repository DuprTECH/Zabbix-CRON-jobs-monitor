// zbxntfy-wrap puts "zbxntfy" in front of existing cron jobs.
//
// Run without arguments in a terminal, it starts a wizard: pick the jobs with
// the arrow keys and space, review the changes, confirm. With job names (or
// -all) it shows the changes, and writes them with -apply.
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

Puts zbxntfy in front of cron jobs that are not monitored yet.

  zbxntfy-wrap                          wizard: pick jobs, review, confirm
  zbxntfy-wrap -list                    list jobs that are not monitored
  zbxntfy-wrap zabbix-dump laps-runner  show the change for these jobs
  zbxntfy-wrap -apply zabbix-dump       back up the crontab, then change it
  zbxntfy-wrap -all -exclude '^cron\.' -apply

Options:
`

type config struct {
	notify     string
	backupRoot string
	wrapper    string
}

func main() {
	fs := flag.NewFlagSet("zbxntfy-wrap", flag.ExitOnError)
	all := fs.Bool("all", false, "all jobs that are not monitored yet")
	exclude := fs.String("exclude", "", "with -all: skip jobs whose name matches this `regex`")
	list := fs.Bool("list", false, "only list jobs that are not monitored (no wizard)")
	apply := fs.Bool("apply", false, "write the changes (crontabs are backed up first)")
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
	if len(names) == 0 && !*all {
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

func unmonitored(jobs []crontab.Job) []crontab.Job {
	var out []crontab.Job
	for _, j := range jobs {
		if j.Monitored == 0 {
			out = append(out, j)
		}
	}
	return out
}

func listJobs(jobs []crontab.Job) int {
	cands := unmonitored(jobs)
	if len(cands) == 0 {
		fmt.Println("All cron jobs are monitored by zbxntfy.")
		return 0
	}
	fmt.Println("Jobs not monitored by zbxntfy:")
	for _, j := range cands {
		fmt.Printf("  %-30s %s:%d\n  %-30s %s %s\n", j.Name, j.FilePath(), j.LineNo(), "", j.Schedule, j.Command)
	}
	fmt.Println("\nShow the change:   zbxntfy-wrap <job> [job ...]   (or -all)")
	fmt.Println("Make the change:   zbxntfy-wrap -apply <job> [job ...]")
	return 0
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
	byName := map[string]crontab.Job{}
	for _, j := range jobs {
		byName[j.Name] = j
	}
	var selected []crontab.Job
	rc := 0
	if all {
		for _, j := range unmonitored(jobs) {
			if excl == nil || !excl.MatchString(j.Name) {
				selected = append(selected, j)
			}
		}
	}
	for _, n := range names {
		j, ok := byName[n]
		switch {
		case !ok:
			fmt.Fprintf(os.Stderr, "zbxntfy-wrap: no cron job named %q (run with -list to see them)\n", n)
			rc = 1
		case j.Monitored == 1:
			fmt.Printf("%s: already monitored\n", n)
		default:
			selected = append(selected, j)
		}
	}

	edits, prc := plan(selected, c)
	if prc != 0 {
		rc = prc
	}
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

// plan computes and prints the line changes.
func plan(selected []crontab.Job, c config) ([]crontab.Edit, int) {
	opts := crontab.WrapOptions{Wrapper: c.wrapper, Notify: c.notify}
	var edits []crontab.Edit
	rc := 0
	for _, j := range selected {
		nl, err := crontab.PlanWrap(j, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: skipped: %v\n", j.Name, err)
			rc = 1
			continue
		}
		fmt.Printf("%s:%d  %s\n  - %s\n  + %s\n", j.FilePath(), j.LineNo(), j.Name,
			crontab.MaskSecrets(strings.TrimSpace(j.RawLine())), crontab.MaskSecrets(strings.TrimSpace(nl)))
		edits = append(edits, crontab.Edit{Job: j, NewLine: nl})
	}
	return edits, rc
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
		fmt.Printf("changed %s (%d job(s))\n", r.File, r.Edits)
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

// wizard: pick jobs, choose a notification tag, review, confirm, apply.
func wizard(jobs []crontab.Job, c config) int {
	cands := unmonitored(jobs)
	if len(cands) == 0 {
		fmt.Println("All cron jobs are monitored by zbxntfy.")
		return 0
	}
	monitored := len(jobs) - len(cands)

	picked, ok := pick(cands, monitored)
	if !ok {
		fmt.Println("Cancelled, nothing changed.")
		return 0
	}
	var selected []crontab.Job
	for i, p := range picked {
		if p {
			selected = append(selected, cands[i])
		}
	}
	if len(selected) == 0 {
		fmt.Println("No job selected, nothing changed.")
		return 0
	}

	in := bufio.NewReader(os.Stdin)
	if c.notify == "" {
		fmt.Print("Notification tag for alerts (zbxntfy -t <value>), Enter = default: ")
		line, _ := in.ReadString('\n')
		c.notify = strings.TrimSpace(line)
	}

	fmt.Println()
	edits, _ := plan(selected, c)
	if len(edits) == 0 {
		return 1
	}
	fmt.Printf("\nApply %d change(s)? The crontabs are backed up first. [y/N]: ", len(edits))
	line, _ := in.ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" && a != "a" && a != "ano" {
		fmt.Println("Nothing changed.")
		return 0
	}
	return applyEdits(edits, c)
}

// pick shows a checkbox list. It returns the selection and false when the
// user cancelled.
func pick(jobs []crontab.Job, monitored int) ([]bool, bool) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zbxntfy-wrap: cannot use the terminal:", err)
		return nil, false
	}
	out := os.Stdout
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l") // alternate screen, hide cursor
	defer func() {
		fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
		_ = term.Restore(fd, old)
	}()

	sel := make([]bool, len(jobs))
	cur, top := 0, 0
	buf := make([]byte, 16)
	for {
		w, h, err := term.GetSize(int(out.Fd()))
		if err != nil || w < 40 || h < 10 {
			w, h = 100, 30
		}
		rows := h - 6
		if cur < top {
			top = cur
		}
		if cur >= top+rows {
			top = cur - rows + 1
		}

		var b strings.Builder
		b.WriteString("\x1b[H\x1b[2J")
		b.WriteString("\x1b[1mSelect cron jobs to monitor with zbxntfy\x1b[0m")
		if monitored > 0 {
			fmt.Fprintf(&b, "  (%d already monitored)", monitored)
		}
		b.WriteString("\r\n\x1b[2m↑/↓ move   space select   a all   n none   enter continue   q quit\x1b[0m\r\n\r\n")
		for i := top; i < len(jobs) && i < top+rows; i++ {
			j := jobs[i]
			box := "[ ]"
			if sel[i] {
				box = "[\x1b[32mx\x1b[0m]"
			}
			text := fmt.Sprintf("%-28s %-15s %s", trunc(j.Name, 28), trunc(j.Schedule, 15), j.Command)
			line := fmt.Sprintf(" %s %s", box, trunc(text, w-7))
			if i == cur {
				line = "\x1b[7m>" + line + "\x1b[0m"
			} else {
				line = " " + line
			}
			b.WriteString(line + "\r\n")
		}
		n := 0
		for _, s := range sel {
			if s {
				n++
			}
		}
		fmt.Fprintf(&b, "\r\n%d of %d selected   %s (%s)", n, len(jobs), trunc(jobs[cur].FilePath(), w-30), jobs[cur].User)
		fmt.Fprint(out, b.String())

		k, err := os.Stdin.Read(buf)
		if err != nil {
			return nil, false
		}
		switch key := string(buf[:k]); key {
		case "\x1b[A", "k":
			if cur > 0 {
				cur--
			}
		case "\x1b[B", "j":
			if cur < len(jobs)-1 {
				cur++
			}
		case "\x1b[5~": // page up
			cur = max(0, cur-rows)
		case "\x1b[6~": // page down
			cur = min(len(jobs)-1, cur+rows)
		case " ", "x":
			sel[cur] = !sel[cur]
			if cur < len(jobs)-1 {
				cur++
			}
		case "a":
			for i := range sel {
				sel[i] = true
			}
		case "n":
			for i := range sel {
				sel[i] = false
			}
		case "\r", "\n":
			return sel, true
		case "q", "\x03", "\x1b":
			return nil, false
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
