// zbxntfy-discovery lists all cron jobs on the host as JSON for the Zabbix
// agent (UserParameter zbxntfy.jobs): LLD data, schedule (previous/next run)
// and whether each job is wrapped with zbxntfy.
//
// It only reads crontabs. To read user crontabs in /var/spool/cron it needs
// CAP_DAC_READ_SEARCH (setcap) or sudo; without it, the unreadable files are
// listed in "errors".
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/DuprTECH/Zabbix-CRON-jobs-monitor/internal/crontab"
)

var version = "dev"

type times struct {
	Prev int64 `json:"prev"`
	Next int64 `json:"next"`
}

type output struct {
	Jobs     []crontab.Job    `json:"jobs"`
	Schedule map[string]times `json:"schedule"`
	Summary  struct {
		Total       int `json:"total"`
		Monitored   int `json:"monitored"`
		Unmonitored int `json:"unmonitored"`
	} `json:"summary"`
	Errors string `json:"errors"`
}

func main() {
	pretty := flag.Bool("pretty", false, "indent JSON output")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("zbxntfy-discovery", version)
		return
	}

	jobs, errs := crontab.Scanner{}.Scan()
	now := time.Now()

	out := output{Jobs: jobs, Schedule: map[string]times{}, Errors: strings.Join(errs, "\n")}
	if out.Jobs == nil {
		out.Jobs = []crontab.Job{}
	}
	for i := range jobs {
		p, n := jobs[i].Times(now)
		out.Schedule[jobs[i].Name] = times{Prev: p, Next: n}
		out.Summary.Total++
		if jobs[i].Monitored == 1 {
			out.Summary.Monitored++
		} else {
			out.Summary.Unmonitored++
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if *pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
