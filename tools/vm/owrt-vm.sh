#!/bin/sh
# Test bench for olcWRT: OpenWrt x86-64 in QEMU with a LAN client.
#
#   LAN client (netns "lanc", 192.168.1.50) ─┐
#   host (192.168.1.2)                ─ br-owrt ─ tap-owrt ─ OpenWrt eth0 (LAN, 192.168.1.1)
#                                              OpenWrt eth1 (WAN) ─ QEMU user networking ─ internet
#
# Run as root (on WSL: wsl -u root). Usage:
#   owrt-vm.sh image <file.img.gz> [size]   unpack and grow an ext4-combined image
#   owrt-vm.sh start [image]                set up networking and boot the VM
#   owrt-vm.sh stop                         power off the VM and remove networking
#   owrt-vm.sh ssh [command...]             run a command on the router
#   owrt-vm.sh put <file> <remote path>     copy a file to the router
#   owrt-vm.sh console <line>               type a line on the serial console
#   owrt-vm.sh lan <command...>             run a command on the LAN client
#   owrt-vm.sh forward <port>               expose LuCI on 0.0.0.0:<port> of the host
set -eu

DIR=${OWRT_VM_DIR:-/root/olcwrt-vm}
IMG=$DIR/openwrt.img
BR=br-owrt
TAP=tap-owrt
NS=lanc
ROUTER=192.168.1.1
mkdir -p "$DIR"
[ -f "$DIR"/id ] || ssh-keygen -q -t ed25519 -N '' -f "$DIR"/id
SSH_OPTS="-i $DIR/id -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5"

# console sends a line to the serial console of the VM.
console() {
	printf '%s\n' "$1" | socat - UNIX-CONNECT:"$DIR"/serial.sock
}

net_up() {
	ip link show "$BR" >/dev/null 2>&1 && return 0
	ip link add "$BR" type bridge
	ip addr add 192.168.1.2/24 dev "$BR"
	ip link set "$BR" up
	ip tuntap add "$TAP" mode tap
	ip link set "$TAP" master "$BR" up
	ip netns add "$NS"
	ip link add veth-owrt type veth peer name veth-lanc
	ip link set veth-owrt master "$BR" up
	ip link set veth-lanc netns "$NS"
	ip -n "$NS" addr add 192.168.1.50/24 dev veth-lanc
	ip -n "$NS" link set lo up
	ip -n "$NS" link set veth-lanc up
	ip -n "$NS" route add default via "$ROUTER"
	mkdir -p /etc/netns/"$NS"
	echo "nameserver $ROUTER" > /etc/netns/"$NS"/resolv.conf
}

net_down() {
	ip netns del "$NS" 2>/dev/null || true
	ip link del veth-owrt 2>/dev/null || true
	ip link del "$TAP" 2>/dev/null || true
	ip link del "$BR" 2>/dev/null || true
}

case "${1:-}" in
image)
	src=$2
	size=${3:-1G}
	gunzip -c "$src" > "$IMG" 2>/dev/null || [ -s "$IMG" ]
	qemu-img resize -f raw "$IMG" "$size" >/dev/null
	echo ", +" | sfdisk --no-reread -q -N 2 "$IMG"
	loop=$(losetup -fP --show "$IMG")
	e2fsck -fy "${loop}p2" >/dev/null 2>&1 || true
	resize2fs "${loop}p2" >/dev/null 2>&1
	losetup -d "$loop"
	echo "image ready: $IMG ($size)"
	;;
start)
	img=${2:-$IMG}
	net_up
	qemu-system-x86_64 -m 512 -smp 2 -accel tcg,thread=multi \
		-drive file="$img",format=raw,if=virtio \
		-netdev tap,id=lan,ifname="$TAP",script=no,downscript=no \
		-device virtio-net-pci,netdev=lan,mac=52:54:00:12:34:56 \
		-netdev user,id=wan \
		-device virtio-net-pci,netdev=wan,mac=52:54:00:12:34:57 \
		-display none -monitor none \
		-chardev socket,id=ser0,path="$DIR"/serial.sock,server,nowait,logfile="$DIR"/console.log \
		-serial chardev:ser0 \
		-daemonize -pidfile "$DIR"/qemu.pid
	echo "booting, console: $DIR/console.log"
	# A fresh OpenWrt accepts root without a password. If a password was
	# set, authorise our key through the serial console instead.
	i=0
	until ssh $SSH_OPTS root@$ROUTER true 2>/dev/null; do
		i=$((i + 1))
		[ $i -lt 200 ] || { echo "ssh to the router failed"; exit 1; }
		if [ $i -eq 100 ]; then
			console ""
			console "mkdir -p /etc/dropbear && echo '$(cat "$DIR"/id.pub)' > /etc/dropbear/authorized_keys && chmod 600 /etc/dropbear/authorized_keys"
		fi
		sleep 3
	done
	echo "router is up"
	;;
console)
	console "$2"
	;;
stop)
	ssh $SSH_OPTS root@$ROUTER poweroff 2>/dev/null || true
	sleep 3
	[ -f "$DIR"/qemu.pid ] && kill "$(cat "$DIR"/qemu.pid)" 2>/dev/null || true
	pkill -f "socat TCP-LISTEN" 2>/dev/null || true
	net_down
	;;
ssh)
	shift
	exec ssh $SSH_OPTS root@$ROUTER "$@"
	;;
put)
	# put <local file> <remote path>
	ssh $SSH_OPTS root@$ROUTER "cat > '$3'" < "$2"
	;;
lan)
	shift
	exec ip netns exec "$NS" "$@"
	;;
forward)
	port=${2:-8081}
	# setsid: the listener must outlive the (WSL) session that started it.
	setsid socat TCP-LISTEN:"$port",fork,reuseaddr TCP:$ROUTER:80 >/dev/null 2>&1 < /dev/null &
	sleep 1
	echo "LuCI: http://localhost:$port/"
	;;
*)
	sed -n '2,17p' "$0"
	exit 2
	;;
esac
