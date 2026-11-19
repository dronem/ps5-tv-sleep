#!/bin/sh
# Test bench for finding a TV power-off method after which the PS5 can still
# wake the TV over HDMI-CEC. Runs inside the lab container; see lab/README.md.
set -eu

: "${TV_HOST:?TV_HOST is not set; the lab reads it from ../.env}"
SERIAL="$TV_HOST:${TV_PORT:-5555}"
OUT=/out
# How long a trial waits for the TV to wake up after it went to sleep.
WAKE_WAIT=${WAKE_WAIT:-180}

# The first adb call starts the adb server, which can outlast a short timeout.
adb start-server >/dev/null 2>&1 || true

say() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*"; }
die() {
	say "error: $*" >&2
	exit 1
}

# tv runs a command on the TV without a pty, so the output has no \r.
tv() { adb -s "$SERIAL" shell -T "$@"; }

# connect makes sure there is a working adb session. A TV in standby does not
# answer, so every step has a timeout.
connect() {
	if [ "$( (timeout 3 adb -s "$SERIAL" get-state; exit $?) 2>/dev/null)" = device ]; then
		return 0
	fi
	adb disconnect "$SERIAL" >/dev/null 2>&1 || true
	out=$( (timeout "${1:-10}" adb connect "$SERIAL" 2>&1; exit $?) 2>/dev/null) || true
	case $out in
	*"connected to"*) ;;
	*) return 1 ;;
	esac
	state=$( (timeout 5 sh -c "adb -s $SERIAL wait-for-device >/dev/null 2>&1; adb -s $SERIAL get-state 2>&1"; exit $?) 2>/dev/null) || true
	case $state in
	device) return 0 ;;
	*unauthorized*) die "the TV has not approved this lab's adb key: run 'hello' with the TV on" ;;
	*) return 1 ;;
	esac
}

need_tv() { connect || die "the TV does not answer on $SERIAL; turn it on"; }

# wakefulness prints Awake, Asleep, Dreaming or Dozing, or nothing if the TV
# does not answer.
wakefulness() {
	(timeout 8 adb -s "$SERIAL" shell -T dumpsys power; exit $?) 2>/dev/null |
		sed -n 's/.*mWakefulness=\([A-Za-z]*\).*/\1/p' | head -n 1
}

# --- ways to turn the TV off ---------------------------------------------------

off_key() {
	[ $# -eq 1 ] || die "usage: key KEYCODE_POWER|KEYCODE_SLEEP|<number>"
	say "input keyevent $1"
	tv input keyevent "$1"
}

off_event() {
	[ $# -ge 1 ] || die "usage: event /dev/input/eventN [linux key code, default 116 = KEY_POWER]"
	dev=$1 code=${2:-116}
	say "sendevent $dev key $code down/up"
	tv "sendevent $dev 1 $code 1 && sendevent $dev 0 0 0 && sendevent $dev 1 $code 0 && sendevent $dev 0 0 0"
}

# replay_cmds turns lines recorded by "getevent -t", such as
#   [   1234.567890] /dev/input/event3: 0001 0074 00000001
# into one chain of sendevent commands.
replay_cmds() {
	tr -d '\r' <"$1" | awk -v dev="${2:-}" '
		function hex(s,   i, n) {
			s = tolower(s); n = 0
			for (i = 1; i <= length(s); i++) n = n * 16 + index("0123456789abcdef", substr(s, i, 1)) - 1
			return n
		}
		match($0, /\/dev\/input\/event[0-9]+: [0-9a-f]+ [0-9a-f]+ [0-9a-f]+/) {
			split(substr($0, RSTART, RLENGTH), f, /:? /)
			if (dev != "" && f[1] != dev) next
			v = hex(f[4]); if (v > 2147483647) v -= 4294967296
			printf "%ssendevent %s %d %d %d", sep, f[1], hex(f[2]), hex(f[3]), v
			sep = " && "
		}'
}

off_replay() {
	[ $# -ge 1 ] || die "usage: replay <recording name or file> [/dev/input/eventN]"
	file=$1
	[ -f "$file" ] || file="$OUT/$1.events"
	[ -f "$file" ] || die "no recording $1 (looked for $file)"
	cmds=$(replay_cmds "$file" "${2:-}")
	[ -n "$cmds" ] || die "no events in $file"
	say "replaying $(printf '%s' "$cmds" | grep -o sendevent | wc -l) events from $file"
	tv "$cmds"
}

# --- commands --------------------------------------------------------------------

cmd_help() {
	cat <<'EOF'
TV test bench. Run commands as "task tv -- <command>" on the host,
or as "tv <command>" inside the container (task lab:shell).

Setup and state
  hello                     connect and wait for the TV to approve this lab's adb key
  state                     power state, last sleep/wake reasons, foreground app
  devices                   input devices, and whether adb may write to them
  cec                       HDMI-CEC settings (full dump saved to out/cec.txt)
  cec-wake [on|off]         show or set tv_wake_on_one_touch_play: wake up when the
                            PS5 sends <Text View On>
  snapshot NAME             save power, CEC, settings and props to out/NAME/

Finding the remote's power button
  watch                     print input events live; press buttons on the remote
  record NAME               same, and save them to out/NAME.events for replay

Turning the TV off once
  key KEY                   adb shell input keyevent KEY (what the service does now)
  event DEV [CODE]          sendevent KEY_POWER (or CODE) down/up to an input device
  replay NAME [DEV]         replay a recording, optionally only one device's events

Watching the whole system
  monitor [SECONDS]         print every PS5 and TV state change; run it, turn the PS5
                            off, wait for the service to turn the TV off, turn it on

Trials: turn the TV off, then wait for you to turn on the PS5
  trial remote              control run: you press power on the remote
  trial key KEY             e.g. trial key KEYCODE_POWER
  trial event DEV [CODE]
  trial replay NAME [DEV]
  history                   results of all trials

Logs and the rest
  logs NAME                 save the TV's logcat to out/NAME.logcat and show power lines
  shell                     interactive adb shell on the TV
  adb ARGS...               any adb command against the TV

Put the PS5 in rest mode before a trial. WAKE_WAIT (default 180s) sets how long
a trial waits for the TV to wake up.
EOF
}

cmd_hello() {
	adb disconnect "$SERIAL" >/dev/null 2>&1 || true
	timeout 15 adb connect "$SERIAL" || die "the TV does not answer; turn it on and check network debugging"
	say "if the TV shows a debugging prompt, choose 'Always allow from this computer'"
	timeout 120 adb -s "$SERIAL" wait-for-device || die "the TV did not approve the key within 2 minutes"
	say "adb connection ok: $(tv getprop ro.product.model) / Android $(tv getprop ro.build.version.release)"
}

cmd_state() {
	need_tv
	tv dumpsys power | grep -E 'mWakefulness=|Last(Global)?(Sleep|Wake)(Reason|Time)|mLastSleepReason|mLastWakeReason|mDisplayReady|mHalInteractiveModeEnabled' || true
	tv dumpsys window | grep -E 'mCurrentFocus=|mFocusedApp=' || true
}

cmd_devices() {
	need_tv
	echo "adb shell user: $(tv id)"
	tv ls -l /dev/input
	echo
	tv getevent -lp | grep -E '^add device|^  name:' | tr -d '\r'
}

cmd_cec() {
	need_tv
	tv dumpsys hdmi_control >"$OUT/cec.txt" 2>&1 || true
	for name in hdmi_cec_enabled hdmi_cec_version power_control_mode \
		power_state_change_on_active_source_lost tv_wake_on_one_touch_play \
		tv_send_standby_on_sleep; do
		printf '%-42s %s\n' "$name" "$(tv cmd hdmi_control cec_setting get "$name" 2>&1 | tail -n 1)"
	done
	echo
	echo "settings global:"
	tv settings list global | grep -iE 'hdmi|cec' || echo "  (none)"
	echo
	echo "full dumpsys hdmi_control: lab/out/cec.txt"
}

cec_get() { tv cmd hdmi_control cec_setting get "$1" 2>&1 | tail -n 1; }

cmd_cec_wake() {
	need_tv
	case ${1:-} in
	"") ;;
	on) tv cmd hdmi_control cec_setting set tv_wake_on_one_touch_play 1 ;;
	off) tv cmd hdmi_control cec_setting set tv_wake_on_one_touch_play 0 ;;
	*) die "usage: cec-wake [on|off]" ;;
	esac
	echo "tv_wake_on_one_touch_play: $(cec_get tv_wake_on_one_touch_play)"
}

cmd_snapshot() {
	[ $# -eq 1 ] || die "usage: snapshot NAME"
	need_tv
	dir="$OUT/$1"
	mkdir -p "$dir"
	tv dumpsys power >"$dir/power.txt" 2>&1 || true
	tv dumpsys hdmi_control >"$dir/hdmi_control.txt" 2>&1 || true
	tv dumpsys display >"$dir/display.txt" 2>&1 || true
	tv settings list global >"$dir/settings-global.txt" 2>&1 || true
	tv settings list secure >"$dir/settings-secure.txt" 2>&1 || true
	tv getprop >"$dir/getprop.txt" 2>&1 || true
	say "saved to lab/out/$1/"
}

cmd_watch() {
	need_tv
	say "press buttons on the remote; Ctrl+C to stop"
	adb -s "$SERIAL" shell getevent -lt
}

cmd_record() {
	[ $# -eq 1 ] || die "usage: record NAME"
	need_tv
	file="$OUT/$1.events"
	say "press power on the remote; the stream stops when the TV goes to sleep (or press Ctrl+C)"
	adb -s "$SERIAL" shell getevent -t | tee "$file" || true
	say "saved to lab/out/$1.events; replay it with: task tv -- trial replay $1"
}

cmd_logs() {
	[ $# -eq 1 ] || die "usage: logs NAME"
	need_tv
	file="$OUT/$1.logcat"
	tv logcat -d -b all >"$file" 2>&1 || true
	grep -iE 'power(manager|key)|gotosleep|wakeup|standby|suspend|hdmi|cec|str_' "$file" | tail -n 60 || true
	say "full log: lab/out/$1.logcat"
}

# wait_sleep waits for the TV to leave Awake. A TV that stops answering twice in
# a row counts as asleep: it closes the adb session as it goes to sleep.
wait_sleep() {
	misses=0 i=0
	while [ $i -lt 30 ]; do
		w=$(wakefulness)
		case $w in
		Asleep | Dozing) return 0 ;;
		Awake | Dreaming) misses=0 ;;
		*)
			misses=$((misses + 1))
			[ $misses -ge 2 ] && return 0
			connect 3 || true
			;;
		esac
		sleep 2
		i=$((i + 1))
	done
	return 1
}

record_trial() {
	printf '%s\t%s\t%s\n' "$(date '+%F %T')" "$1" "$2" >>"$OUT/trials.tsv"
	say "result: $2"
}

cmd_trial() {
	[ $# -ge 1 ] || die "usage: trial remote | key KEY | event DEV [CODE] | replay NAME [DEV]"
	method=$1
	shift
	what="$method${*:+ $*}"
	name="trial-$(date +%Y%m%d-%H%M%S)-$method"

	need_tv
	w=$(wakefulness)
	[ "$w" = Awake ] || die "the TV is ${w:-not answering}, it must be on"
	cec_wake=$(cec_get tv_wake_on_one_touch_play)
	what="$what (cec wake: $cec_wake)"
	say "tv_wake_on_one_touch_play: $cec_wake"
	tv logcat -c -b all >/dev/null 2>&1 || tv logcat -c >/dev/null 2>&1 || true

	case $method in
	remote) say "press power on the TV remote now" ;;
	key) off_key "$@" ;;
	event) off_event "$@" ;;
	replay) off_replay "$@" ;;
	*) die "unknown method $method" ;;
	esac

	if ! wait_sleep; then
		record_trial "$what" "TV did not go to sleep"
		exit 1
	fi
	slept=$(date +%s)
	say "the TV is asleep. Turn on the PS5 now; waiting up to ${WAKE_WAIT}s"

	while [ $(($(date +%s) - slept)) -lt "$WAKE_WAIT" ]; do
		if connect 5 && [ "$(wakefulness)" = Awake ]; then
			record_trial "$what" "WOKE UP $(($(date +%s) - slept))s after sleep"
			sleep 3
			cmd_logs "$name" >/dev/null
			say "logcat saved to lab/out/$name.logcat"
			return 0
		fi
		sleep 5
	done
	record_trial "$what" "did not wake up within ${WAKE_WAIT}s"
	say "wake the TV with the remote, then save the log with: task tv -- logs $name"
}

# ps5_state asks the PS5 over DDP, as the service does: on, standby, garbled
# (a reply that is not a status, sent around a power change) or unreachable.
ps5_state() {
	[ -n "${PS5_HOST:-}" ] || {
		echo "unknown (PS5_HOST not set)"
		return
	}
	for _ in 1 2 3; do
		r=$( (printf 'SRCH * HTTP/1.1\ndevice-discovery-protocol-version:00030010\n' |
			timeout 3 socat -T1 - "UDP:$PS5_HOST:9302" | head -n 1 | tr -d '\r'
			exit 0) 2>/dev/null)
		case $r in
		*" 200 "*) echo on && return ;;
		*" 620 "*) echo standby && return ;;
		?*) echo garbled && return ;;
		esac
	done
	echo unreachable
}

note() {
	line="$(date +%H:%M:%S) $*"
	echo "$line"
	echo "$line" >>"$OUT/monitor.log"
}

cmd_monitor() {
	interval=${1:-3}
	say "watching the PS5 and the TV every ${interval}s (log: lab/out/monitor.log); Ctrl+C to stop"
	echo "--- monitor started $(date '+%F %T')" >>"$OUT/monitor.log"
	last_ps5='' last_tv='' ps5_changed=$(date +%s)
	while :; do
		p=$(ps5_state)
		t=''
		if connect 3; then t=$(wakefulness); fi
		t=${t:-no answer}
		now=$(date +%s)
		if [ "$p" != "$last_ps5" ]; then
			note "ps5: ${last_ps5:-?} -> $p"
			[ -n "$last_ps5" ] && ps5_changed=$now
			last_ps5=$p
		fi
		if [ "$t" != "$last_tv" ]; then
			if [ -n "$last_tv" ]; then
				note "tv:  $last_tv -> $t ($((now - ps5_changed))s after the PS5 became $last_ps5)"
			else
				note "tv:  $t"
			fi
			if [ "$t" = Awake ] && [ -n "$last_tv" ]; then
				echo "     foreground: $(tv dumpsys window 2>/dev/null | sed -n 's/^ *mCurrentFocus=//p' | head -n 1)"
				echo "     last CEC messages:"
				tv dumpsys hdmi_control 2>/dev/null | grep -E '^ +\[(S|R|H)\]' | tail -n 8 | sed 's/^ */       /'
			fi
			last_tv=$t
		fi
		sleep "$interval"
	done
}

cmd_history() {
	[ -f "$OUT/trials.tsv" ] || die "no trials yet"
	cat "$OUT/trials.tsv"
}

cmd_shell() {
	need_tv
	adb -s "$SERIAL" shell
}

cmd_adb() { adb -s "$SERIAL" "$@"; }

[ $# -ge 1 ] || set -- help
cmd=$1
shift
case $cmd in
help | -h | --help) cmd_help ;;
hello | state | devices | cec | snapshot | monitor | watch | record | logs | history | shell | adb | trial) "cmd_$cmd" "$@" ;;
cec-wake) cmd_cec_wake "$@" ;;
key) need_tv && off_key "$@" ;;
event) need_tv && off_event "$@" ;;
replay) need_tv && off_replay "$@" ;;
*) die "unknown command $cmd; see help" ;;
esac
