#!/bin/sh
# deb: $1 = configure, $2 = previous version (empty on first install)
# rpm: $1 = number of installed versions (1 on first install)

# Allow the agent (user zabbix) to read user crontabs in /var/spool/cron.
if command -v setcap >/dev/null 2>&1; then
    setcap cap_dac_read_search+ep /usr/bin/zbxntfy-discovery || \
        echo "zbxntfy: setcap failed; user crontabs will not be discovered" >&2
else
    echo "zbxntfy: setcap not found (install libcap2-bin / libcap); user crontabs will not be discovered" >&2
fi

# Register the UserParameter with every installed agent.
for d in /etc/zabbix/zabbix_agent2.d /etc/zabbix/zabbix_agentd.d; do
    if [ -d "$d" ]; then
        ln -sf /usr/share/zbxntfy/userparameter_zbxntfy.conf "$d/userparameter_zbxntfy.conf"
    fi
done

for s in zabbix-agent2 zabbix-agent; do
    if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet "$s"; then
        systemctl restart "$s" || true
    fi
done

# First install: offer the wizard that puts zbxntfy in front of existing cron
# jobs, but only when installed interactively from a terminal.
first_install=no
case "$1" in
    configure) [ -z "$2" ] && first_install=yes ;;
    1) first_install=yes ;;
esac
if [ "$first_install" = yes ] \
   && [ "${DEBIAN_FRONTEND:-}" != noninteractive ] \
   && [ -z "${ZBXNTFY_NO_WIZARD:-}" ] \
   && [ -t 1 ] && { : </dev/tty; } 2>/dev/null; then
    /usr/bin/zbxntfy-wrap </dev/tty >/dev/tty 2>&1 || true
else
    echo "zbxntfy: run 'zbxntfy-wrap' to choose which cron jobs to monitor."
fi
exit 0
