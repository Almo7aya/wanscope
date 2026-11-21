#!/bin/sh
# wanscope-update: installs a wanscope release. wanscope starts it on its own (it restarts wanscope on the way):
#   wanscope-update <status file> <version> <package URL>...    (the packages in install order)
# Progress goes to the status file as one line of JSON:
#   {"state":"running|done|failed","step":"...","message":"...","version":"...","at":<unix time>}
# No `opkg update`: wanscope's dependencies are installed already, and the feed lists cost megabytes.

STATUS=$1
VERSION=$2
shift 2
DIR=/tmp/wanscope-update.d

say() { # state step message
	msg=$(printf '%s' "$3" | tr '\n\r\t"\\' '     ' | cut -c1-400)
	printf '{"state":"%s","step":"%s","message":"%s","version":"%s","at":%s}\n' "$1" "$2" "$msg" "$VERSION" "$(date +%s)" >"$STATUS.tmp" &&
		mv "$STATUS.tmp" "$STATUS"
}

fetch() { # url file
	uclient-fetch -q -T 60 -O "$2" "$1" 2>/dev/null ||
		wget -q -T 60 -O "$2" "$1" 2>/dev/null ||
		curl -fsSL -m 300 -o "$2" "$1" 2>/dev/null
}

# your settings win over the package's (opkg keeps them too; this is in case it doesn't)
keep_config() { [ -s "$DIR/config" ] && cp -p "$DIR/config" /etc/config/wanscope; }

fail() { # step message
	keep_config
	say failed "$1" "$2"
	rm -rf "$DIR"
	exit 1
}

[ -n "$STATUS" ] && [ -n "$VERSION" ] && [ $# -gt 0 ] || { echo "usage: $0 <status file> <version> <package URL>..." >&2; exit 2; }
# leave wanscope's cgroup: stopping the service must not stop the update
echo $$ >/sys/fs/cgroup/cgroup.procs 2>/dev/null
rm -rf "$DIR"
mkdir -p "$DIR" || fail prepare "can't make $DIR"

files=""
for u in "$@"; do
	f="$DIR/${u##*/}"
	say running download "Downloading ${u##*/}"
	if ! fetch "$u" "$f" || [ ! -s "$f" ]; then
		fail download "Couldn't download ${u##*/}"
	fi
	files="$files $f"
done

# --force-overwrite: a copy installed by hand owns no files, which opkg calls a clash
if command -v apk >/dev/null 2>&1; then
	inst="apk add --allow-untrusted --force-overwrite"
else
	inst="opkg install --force-overwrite"
fi
cp -p /etc/config/wanscope "$DIR/config" 2>/dev/null
for f in $files; do
	say running install "Installing ${f##*/}"
	out=$($inst "$f" 2>&1) || fail install "${f##*/}: $(printf '%s' "$out" | tail -n 3)"
done
keep_config
rm -rf "$DIR"

say running restart "Restarting wanscope"
/etc/init.d/wanscope enable 2>/dev/null
/etc/init.d/wanscope restart 2>/dev/null
rm -f /tmp/luci-indexcache* /tmp/luci-modulecache/* 2>/dev/null
/etc/init.d/rpcd reload 2>/dev/null
say done done "Updated to $VERSION"
