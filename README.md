# zbxntfy — cron job monitoring for Zabbix

Discovers **every** cron job on a host. For jobs you wrap with `zbxntfy`, Zabbix gets:

- exit code
- run time
- output, with alerts for lines matching error or warning regexes
- an alert when the job did not start on schedule

```
0 2 * * *  root  zbxntfy /opt/scripts/backup.sh
0 3 * * *  root  zbxntfy -n backup-db -t IT_ops /opt/scripts/backup.sh --db
```

## Quick start

You do not need to build anything. Every release contains ready-made packages.

### 1. Install on the server (as root)

Debian / Ubuntu:

```sh
cd /tmp && curl -fsSLO https://github.com/DuprTECH/Zabbix-CRON-jobs-monitor/releases/latest/download/zbxntfy_linux_amd64.deb
apt install ./zbxntfy_linux_amd64.deb
sudo -u zabbix zbxntfy-discovery -pretty
```

RHEL / Rocky / Alma:

```sh
cd /tmp && curl -fsSLO https://github.com/DuprTECH/Zabbix-CRON-jobs-monitor/releases/latest/download/zbxntfy_linux_amd64.rpm
dnf install ./zbxntfy_linux_amd64.rpm
sudo -u zabbix zbxntfy-discovery -pretty
```

For ARM servers, replace `amd64` with `arm64`.

The package does the following:

- installs `zbxntfy`, `zbxntfy-discovery` and `zbxntfy-wrap` to `/usr/bin`
- installs `zabbix-sender` if it is missing, from the Zabbix repository, which must be configured on the host
- sets `cap_dac_read_search` on `zbxntfy-discovery`, so the agent can read user crontabs
- links the UserParameter into `/etc/zabbix/zabbix_agent2.d/` and/or `/etc/zabbix/zabbix_agentd.d/`
- restarts the running agent
- on the first interactive install, starts the [wizard](#3-wrap-jobs) that lets you pick the cron jobs to monitor. Skip it with `ZBXNTFY_NO_WIZARD=1 apt install ...`. It is also skipped automatically for non-interactive installs such as Ansible or `DEBIAN_FRONTEND=noninteractive`.

The last command must print all cron jobs as JSON, with an empty `"errors"`.

To upgrade, run the same commands again.

### 2. Import the template in Zabbix

1. Download [zabbix/template_cron_jobs_zbxntfy.yaml](https://raw.githubusercontent.com/DuprTECH/Zabbix-CRON-jobs-monitor/main/zabbix/template_cron_jobs_zbxntfy.yaml). It is also in `/usr/share/zbxntfy/` on the host.
2. Go to *Data collection → Templates → Import* (Zabbix 7.0+).
3. Link **Cron jobs by zbxntfy** to the host.
4. Optional: exclude system jobs with the host macro `{$JOB.NAME.NOT_MATCHES}`, e.g.
   `^(cron\.(hourly|daily|weekly|monthly)|e2scrub_all(_cron)?|sessionclean)$`

After 1–2 minutes, the cron jobs appear in *Latest data*, filtered by tag `component: cron`. Jobs not wrapped yet show up as **not monitored** problems.

### 3. Wrap jobs

Run the wizard as root:

```sh
zbxntfy-wrap
```

```
Select cron jobs to monitor with zbxntfy  (1 already monitored)
↑/↓ move   space select   a all   n none   enter continue   q quit

> [x] zabbix-dump                  40 2 * * *      /opt/./zabbix-dump -q
  [x] suppress_longdown            20 0 * * *      ZBX_KEY=*** python3 /root/zabbix/suppress_longdown.py --apply
  [ ] cron.daily                   25 6 * * *      test -x /usr/sbin/anacron || { cd / && run-parts --report /etc/cron.daily; }
  ...
```

The wizard then:

1. asks for an optional notification tag (`-t`),
2. shows every line change (`-` old, `+` new; secrets are masked),
3. asks for confirmation,
4. backs up the affected crontabs and writes the changes:

```
/var/spool/cron/crontabs/root:12  zabbix-dump
  - 40 2 * * * /opt/./zabbix-dump -q
  + 40 2 * * * /usr/bin/zbxntfy /opt/./zabbix-dump -q

changed /var/spool/cron/crontabs/root (2 job(s))
        backup:  /var/backups/zbxntfy/20261105-174500/var/spool/cron/crontabs/root
        restore: crontab -u root /var/backups/zbxntfy/20261105-174500/var/spool/cron/crontabs/root
```

**not monitored** resolves within a minute. Every run then reports its start, exit code, duration and output.

What `zbxntfy-wrap` does:

- Inserts `zbxntfy` exactly in front of the job's command:
  - after `cd /x &&`
  - after `VAR=value`
  - inside `if …; then` and `{ …; }`
  
  Redirects and everything else stay unchanged.
- Re-parses every changed line and checks that discovery will see it as the same job, now monitored.
- Backs up every affected file to `/var/backups/zbxntfy/<timestamp>/` before writing. Each change prints its own restore command.
- `/etc/crontab` and `/etc/cron.d/*`: replaced atomically, keeping owner and mode.
- User crontabs: installed with `crontab -u <user> -`, so cron reloads them.
- Refuses to write a file that changed since it was read.

Non-interactive use, for scripts and Ansible:

```sh
zbxntfy-wrap -list                                # jobs not monitored yet
zbxntfy-wrap zabbix-dump laps-runner              # show the change only
zbxntfy-wrap -apply zabbix-dump laps-runner       # back up, then change
zbxntfy-wrap -all -exclude '^(cron\.|e2scrub|sessionclean)' -t IT_ops -apply
```

You can also edit the crontab by hand: put `zbxntfy` in front of the command, e.g. `40 2 * * *  zbxntfy /opt/zabbix-dump -q`.

### Test job (optional)

Set the host macro `{$JOB.GRACE:"zbxntfy-test"}` = `1m`, then:

```sh
cat > /etc/cron.d/zbxntfy-test <<'EOT'
*/2 * * * * root zbxntfy -n zbxntfy-test sh -c 'echo "WARNING: test"; echo "ERROR: test"; exit 1'
EOT
```

| Check | Expected |
|---|---|
| ~1 min after creating the file | job `zbxntfy-test` discovered, *Monitored* = 1 |
| after the first run | problems **failed with exit code 1** and **errors in output**; item *Error lines* = `ERROR: test` |
| change the command to `sh -c 'echo OK'` | problems resolve after the next run |
| `systemctl stop cron` for ~4 min (RHEL: `crond`) | **did not start on schedule**; resolves after `systemctl start cron` and the next run |
| `zbxntfy -dry-run -n x sh -c 'echo hi'` | prints the values instead of sending them |

Remove `/etc/cron.d/zbxntfy-test` when done.

## How it works

```
                       ┌──────────────── host ────────────────┐
 Zabbix agent (active) │ zbxntfy.jobs ─► zbxntfy-discovery    │  every 1m: reads /etc/crontab, /etc/cron.d/*,
                       │                 (JSON)               │  /var/spool/cron/** → jobs, schedule, prev/next run
                       │                                      │
 cron ─► zbxntfy ─► cmd│ stdout/stderr passed through + kept  │
            │          │ zabbix_sender: job.start  (at start) │
            └─────────►│                job.log, end,         │
                       │                duration, rc (at end) │
                       └──────────────────────────────────────┘
```

| Piece | Role |
|---|---|
| `zbxntfy-discovery` | Called by the agent through a UserParameter. Lists all cron jobs and marks which ones are wrapped with `zbxntfy`. For each job it also returns when it last should have run and when it runs next. |
| `zbxntfy` | Wrapper used in the crontab. Runs the command and passes its output and exit code through unchanged, so cron mail and redirects keep working. Reports the run to Zabbix through `zabbix_sender`. |
| `zbxntfy-wrap` | Wizard and CLI that put `zbxntfy` in front of existing cron jobs. It backs up the crontabs before writing. |
| Template **Cron jobs by zbxntfy** | Does all the evaluation: LLD, the error/warning regexes, the triggers, and the alert text. The host needs no configuration. |

### Life cycle of a job

1. You link the template. Every cron job is discovered, and jobs without `zbxntfy` raise **"not monitored"** (Warning).
2. You put `zbxntfy` in front of the command. **"not monitored"** resolves on the next discovery run, within about a minute.
3. From the next run on, Zabbix receives its start, exit code, duration and output.
4. If you remove the cron line, the job disappears from Zabbix after the LLD lifetime.

### Triggers

| Trigger | Severity | Condition |
|---|---|---|
| not monitored by zbxntfy | Warning | The cron line does not use `zbxntfy`. |
| failed with exit code N | High | `rc != 0` |
| errors in output | Average | A line matches `{$JOB.ERROR.REGEX}` and does not match `{$JOB.IGNORE.REGEX}`. |
| warnings in output | Warning | A line matches `{$JOB.WARN.REGEX}` and is not an error. |
| did not start on schedule | High | The scheduled time (from cron) has passed by more than `{$JOB.GRACE}` and no start was reported. Weekday-only jobs and `*/5` jobs are handled correctly. |
| running longer than `{$JOB.MAXRUN}` | Average | The job reported a start, no end, and the time limit has passed. |
| no run reported for `{$JOB.SILENT}` | Warning | Safety net, including wrapped jobs that never ran. |
| some crontabs cannot be read or parsed | Warning | Permission problems or syntax errors in a crontab. |

Every alert contains the cron line. Output alerts also contain the matching lines and the last `{$JOB.TAIL.LINES}` lines of output.
Triggers for `@reboot` jobs that depend on the schedule are not created.

## Usage

```
zbxntfy [options] [--] command [args...]

  -n name    job name (default: derived from the command, e.g. /opt/x/backup.sh → backup);
             a full path is allowed: -n /opt/x/backup.sh
  -t value   trigger tag notify:<value> for alert routing (default: "default")
  -c file    agent config for zabbix_sender (default: /etc/zabbix/zabbix_agent2.conf, then zabbix_agentd.conf)
  -s host    host name in Zabbix (default: Hostname from the agent config)
  -z server  Zabbix server/proxy (default: ServerActive from the agent config)
  -m bytes   max output sent to Zabbix, the tail is kept (default 60000)
  -q         do not print zabbix_sender errors
  -dry-run   print the values instead of sending them
```

- `zbxntfy` runs the command directly, without a shell. Pipes and `&&` belong to cron's shell: `cd /x && zbxntfy ./run.sh > /dev/null 2>&1`.
  For inline shell code, use `zbxntfy -n myjob sh -c '...'`.
- The job name is derived the same way in discovery and in the wrapper, so wrapping a job keeps its name and history. Use `-n` when one script runs with different arguments.
- Name derivation looks past `bash x.sh`, `python3 x.py`, `nice`, `ionice`, `timeout`, `flock`, `sudo`, `env VAR=…` and `run-parts DIR`.

### Alert routing (-t)

Every trigger carries the tag `notify:<value>` from `zbxntfy -t <value>`. Create one action per value in Zabbix:

- *Alerts → Actions → Trigger actions*
- Condition: **Tag `notify` equals `IT_ops`**
- Operation: send to user group **IT_ops**

Add one more action for `notify = default`.

## Installation on many hosts

- **Packages:** use the commands from the [Quick start](#1-install-on-the-server-as-root) with any tool: Ansible `apt: deb=<url>` / `dnf: name=<url>`, Semaphore, or a shell loop.
- **Ansible role ([ansible/](ansible/)):** copies locally built binaries, so it needs `make dist` first. It installs to `/usr/local/bin` and supports `zbxntfy_privilege: setcap | sudo | none`. It needs the `community.general` collection.
- **Manual, without a package:** binaries from `zbxntfy_linux_amd64.tar.gz`:
  ```sh
  install -m 0755 zbxntfy zbxntfy-discovery /usr/local/bin/
  setcap cap_dac_read_search+ep /usr/local/bin/zbxntfy-discovery
  cp zabbix/userparameter_zbxntfy.conf /etc/zabbix/zabbix_agent2.d/    # agent 1: zabbix_agentd.d/
  systemctl restart zabbix-agent2
  ```

The host must have active checks working: `ServerActive`, plus `Hostname` equal to the host name in Zabbix.

## Macros

All macros can be overridden per host or per job with context, e.g. `{$JOB.WARN.REGEX:"backup"}`.

| Macro | Default | |
|---|---|---|
| `{$JOB.ERROR.REGEX}` | `\b(error\|errors\|fatal\|critical\|failed\|failure\|exception\|traceback\|panic\|denied)\b` | JavaScript regex, case-insensitive; write `/` as `\/`; empty = off |
| `{$JOB.WARN.REGEX}` | `\bwarn(ing\|ings)?\b` | |
| `{$JOB.IGNORE.REGEX}` | `0 errors`, `no warnings`, `failed: 0`, … | lines that are never errors or warnings |
| `{$JOB.TAIL.LINES}` | `50` | output lines in the alert |
| `{$JOB.GRACE}` | `10m` | delay allowed after the scheduled time |
| `{$JOB.MAXRUN}` | `12h` | |
| `{$JOB.SILENT}` | `8d` | raise it for monthly jobs, e.g. `{$JOB.SILENT:"report"}=32d` |
| `{$JOB.NAME.MATCHES}` / `{$JOB.NAME.NOT_MATCHES}` | `.*` / `CHANGE_IF_NEEDED` | discovery filter, e.g. `^(cron\.(hourly\|daily\|weekly\|monthly)\|e2scrub_all)$` |
| `{$JOB.USER.MATCHES}` / `{$JOB.USER.NOT_MATCHES}` | `.*` / `CHANGE_IF_NEEDED` | |

## Notes and limits

- **Secrets in crontabs:** the command line is sent to Zabbix and appears in alerts as `{#CMD}`. Likely secrets are replaced by `***` before sending: `*KEY=`, `*TOKEN=`, `*PASSWORD=`, `--password`, `--api-key`, `-pSECRET` and `user:pass@` in URLs. Keeping secrets out of crontabs, e.g. in an env file read by the script, is still better.
- **User crontabs** (`crontab -e`) live in `/var/spool/cron` and only root can read them. Without `setcap` or sudo, discovery sees only `/etc/crontab` and `/etc/cron.d`, and the "cannot read" trigger tells you so.
- **TLS PSK:** `zabbix_sender` uses the TLS settings from the agent config. A job that runs as a non-root user must be able to read the PSK file, e.g. by being a member of group `zabbix`.
- **Output size:** at most 60000 bytes (the tail) are sent. This stays within the MySQL `history_text` limit. Errors earlier in very long output are cut off.
- **Time zone:** schedules are computed in the server's local time, or in `CRON_TZ` if it is set in the crontab.
- **Item keys:** the job name is sanitized to `[A-Za-z0-9_./-]`; other characters (space, comma, brackets, quotes) become `_`.
- **Not discovered:** systemd timers and anacron-only jobs are not discovered yet. `/etc/cron.daily` and the other run-parts directories show up as a single job, e.g. `cron.daily`.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `curl: (22) ... 404` right after a new release | GitHub Actions is still building the release (1–2 min). Wait until the *Actions* run is green. |
| Template import keeps failing with an error that was already fixed | The browser saved an old cached copy, e.g. `template_cron_jobs_zbxntfy (1).yaml`. Download it again under a new name, or take it from `/usr/share/zbxntfy/`. |
| *Execute now* → `wrong master item type` | Expected: all items come from the active agent or `zabbix_sender`, so Zabbix cannot poll them. Wait for the next minute, or run the job. |
| *Cron jobs (raw)* has no data | Check `zabbix_agent2 -t zbxntfy.jobs`, `Include=` in the agent config, `Hostname=`/`ServerActive=`, and `/var/log/zabbix/zabbix_agent2.log`. |
| `zbxntfy: zabbix_sender ... failed` printed by the wrapper | The job is not discovered yet, or `Hostname` does not match the host in Zabbix. |
| *Error lines* item not supported | The regex macro is not a valid JavaScript regex. See [Macros](#macros). |

## Development

Only needed when you change the code. Requires Go 1.22+ or Docker.

```sh
git clone https://github.com/DuprTECH/Zabbix-CRON-jobs-monitor.git && cd Zabbix-CRON-jobs-monitor
make test     # go vet + unit tests (cron math, crontab parsing, name derivation, secret masking, crontab rewriting)
make dist     # dist/amd64, dist/arm64
```

## Releasing

```sh
git tag v0.1.2 && git push origin v0.1.2
```

GitHub Actions ([.github/workflows/release.yml](.github/workflows/release.yml)) runs the tests and then goreleaser. The release gets `zbxntfy_linux_{amd64,arm64}.{deb,rpm,tar.gz}` and `checksums.txt`. The `releases/latest/download/...` links in this README always point to the newest release.

## Support

Need something extra? I can extend or customize it for your company's needs, for example systemd timers, other alerting backends, or rollout through your own repositories. Feel free to get in touch: 📧 [info@duprtech.sk](mailto:info@duprtech.sk)

If this work makes sense to you, give the repo a ⭐ star or support me on Ko-fi ☕

I'm adding more tools and templates over time, so feel free to [follow me on GitHub](https://github.com/DuprTECH) to see what's new.

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/duprtech)

## License

[MIT](LICENSE)
