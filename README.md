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

## Quick start (test on one server)

### Get the binaries

**Option A: download a release.** No build needed. Each `v*` tag is built by GitHub Actions.

```sh
ARCH=amd64     # or arm64
curl -fsSLO https://github.com/DuprTECH/Zabbix-CRON-jobs-monitor/releases/latest/download/zbxntfy_linux_$ARCH.tar.gz
mkdir zbxntfy && tar -xzf zbxntfy_linux_$ARCH.tar.gz -C zbxntfy && cd zbxntfy
mkdir -p dist/$ARCH && mv zbxntfy zbxntfy-discovery dist/$ARCH/     # same layout as a local build
```

Packages are also available: `zbxntfy_linux_amd64.deb` and `zbxntfy_linux_amd64.rpm`. They install to `/usr/bin`, set up setcap and the UserParameter. Install with `apt install ./zbxntfy_linux_amd64.deb`.

If the repository is private, the download needs a token, e.g. `gh release download`.

**Option B: build from source.** Needs Go 1.22+ or Docker:

```sh
git clone https://github.com/DuprTECH/Zabbix-CRON-jobs-monitor.git
cd Zabbix-CRON-jobs-monitor
make test dist          # without make: CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/amd64/ ./cmd/...
# or, with Docker only:
docker run --rm -v "$PWD":/src -w /src golang:1.23 sh -c 'go test ./... && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/amd64/ ./cmd/...'
```

The result is `dist/amd64/zbxntfy` and `dist/amd64/zbxntfy-discovery`, for `x86_64` servers. Use `arm64` for ARM.

### Install

On the server, as root, in the extracted or cloned directory:

```sh
install -m 0755 dist/amd64/zbxntfy dist/amd64/zbxntfy-discovery /usr/local/bin/
apt install zabbix-sender libcap2-bin           # RHEL: dnf install zabbix-sender libcap
setcap cap_dac_read_search+ep /usr/local/bin/zbxntfy-discovery
sudo -u zabbix /usr/local/bin/zbxntfy-discovery -pretty     # should list all cron jobs, empty "errors"

cp zabbix/userparameter_zbxntfy.conf /etc/zabbix/zabbix_agent2.d/   # agent 1: /etc/zabbix/zabbix_agentd.d/
systemctl restart zabbix-agent2
```

In Zabbix:

1. Import [zabbix/template_cron_jobs_zbxntfy.yaml](zabbix/template_cron_jobs_zbxntfy.yaml) and link **Cron jobs by zbxntfy** to the host.
2. Set the host macro `{$JOB.GRACE:"zbxntfy-test"}` = `1m`.

After 1–2 minutes, the existing jobs appear in *Latest data* and as **not monitored** problems.

Test job:

```sh
cat > /etc/cron.d/zbxntfy-test <<'EOF'
*/2 * * * * root /usr/local/bin/zbxntfy -n zbxntfy-test sh -c 'echo "WARNING: test"; echo "ERROR: test"; exit 1'
EOF
```

| Check | Expected |
|---|---|
| ~1 min after creating the file | job `zbxntfy-test` discovered, *Monitored* = 1 |
| after the first run | problems **failed with exit code 1** and **errors in output**; item *Error lines* = `ERROR: test` |
| change the command to `sh -c 'echo OK'` | problems resolve after the next run |
| `systemctl stop cron` for ~4 min (RHEL: `crond`) | **did not start on schedule**; resolves after `systemctl start cron` and the next run |
| `zbxntfy -dry-run -n x sh -c 'echo hi'` | prints the values instead of sending them |

Remove `/etc/cron.d/zbxntfy-test` when done. For many hosts, use the [Ansible role](#2-host--ansible-also-from-semaphore).

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

## Installation

### 1. Template

Import [zabbix/template_cron_jobs_zbxntfy.yaml](zabbix/template_cron_jobs_zbxntfy.yaml) (Zabbix 7.0+) and link it to the hosts.
The host must have active checks working: `ServerActive` and `Hostname` in the agent config.

### 2. Host — Ansible (also from Semaphore)

```sh
make dist                                   # dist/amd64, dist/arm64
ansible-playbook -i inventory ansible/site.yml
```

The role does the following:

- installs `zabbix-sender`
- copies the binaries to `/usr/local/bin`
- sets `cap_dac_read_search` on `zbxntfy-discovery`
- installs the UserParameter for agent and/or agent 2, then restarts the agent
- checks that discovery works as user `zabbix`

Variables are in [ansible/roles/zbxntfy/defaults/main.yml](ansible/roles/zbxntfy/defaults/main.yml). `zbxntfy_privilege` can be `setcap`, `sudo` or `none`.
The role needs the `community.general` collection.

### 2b. Host — .deb / .rpm

`goreleaser release` builds packages that install to `/usr/bin`. The packages depend on `zabbix-sender`, set the capability, and register the UserParameter in the agent directories that exist.

### 2c. Host — manual

```sh
install -m 0755 zbxntfy zbxntfy-discovery /usr/local/bin/
setcap cap_dac_read_search+ep /usr/local/bin/zbxntfy-discovery
cp zabbix/userparameter_zbxntfy.conf /etc/zabbix/zabbix_agent2.d/
systemctl restart zabbix-agent2
sudo -u zabbix /usr/local/bin/zbxntfy-discovery -pretty     # test
```

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

- **User crontabs** (`crontab -e`) live in `/var/spool/cron` and only root can read them. Without `setcap` or sudo, discovery sees only `/etc/crontab` and `/etc/cron.d`, and the "cannot read" trigger tells you so.
- **TLS PSK:** `zabbix_sender` uses the TLS settings from the agent config. A job that runs as a non-root user must be able to read the PSK file, e.g. by being a member of group `zabbix`.
- **Output size:** at most 60000 bytes (the tail) are sent. This stays within the MySQL `history_text` limit. Errors earlier in very long output are cut off.
- **Time zone:** schedules are computed in the server's local time, or in `CRON_TZ` if it is set in the crontab.
- **Item keys:** the job name is sanitized to `[A-Za-z0-9_./-]`; other characters (space, comma, brackets, quotes) become `_`.
- **Not discovered:** systemd timers and anacron-only jobs are not discovered yet. `/etc/cron.daily` and the other run-parts directories show up as a single job, e.g. `cron.daily`.

## Development

```sh
make test     # go vet + unit tests (cron math, crontab parsing, name derivation)
make dist
```

## Releasing

```sh
git tag v0.1.0 && git push origin v0.1.0
```

GitHub Actions ([.github/workflows/release.yml](.github/workflows/release.yml)) runs the tests and then goreleaser. The release gets `zbxntfy_linux_{amd64,arm64}.tar.gz`, `.deb`, `.rpm` and `checksums.txt`.
