#!/bin/sh
# Builds the reference e2e topology out of network namespaces (see docs/SPEC.md §20).
#
#   n1, n2, n3 - nodes with agents; rt - router (also runs an agent); pod - macvlan "pod" on n3
#   segment A: untagged bridge 10.10.1.0/24, the switch port of rt has MTU 1400
#   segment B: VLAN 300 over a trunk bridge, 10.30.0.0/24, tbf ~900 Mbit/s,
#              TCP to n2 is redirected (transparent proxy): ICMP ok, TCP broken
#   segment C: VLAN 301, 10.31.0.0/24, tbf 100 Mbit/s; n3 holds its address on the macvlan
#              parent while the pod sits on a macvlan child (parent cannot reach child)
#   management: bridge lsm-mgmt 192.168.250.0/24 in the root namespace (excluded by agents)
#
# usage: topo.sh up|down
# E2E_NO_VLAN=1 replaces VLAN sub-interfaces with separate bridges for kernels without 8021q.
set -eu

P=${E2E_PREFIX:-lse}
NODES="n1 n2 n3 rt"
NO_VLAN=${E2E_NO_VLAN:-0}


nsx() { _ns=$1; shift; ip netns exec "$P-$_ns" "$@"; }

down() {
    for n in $NODES pod sw; do ip netns del "$P-$n" 2>/dev/null || true; done
    for n in $NODES pod; do ip link del "m-$P-$n" 2>/dev/null || true; done
    ip link del "$P-mgmt" 2>/dev/null || true
}

idx() {
    case $1 in n1) echo 1 ;; n2) echo 2 ;; n3) echo 3 ;; rt) echo 254 ;; pod) echo 13 ;; esac
}

vlan_supported() {
    ip netns add "$P-probe" 2>/dev/null || return 1
    ok=1
    if ip -n "$P-probe" link add pv0 type dummy 2>/dev/null &&
        ip -n "$P-probe" link add link pv0 name pv0.2 type vlan id 2 2>/dev/null; then
        ok=0
    fi
    ip netns del "$P-probe"
    return $ok
}

up() {
    down
    OUT=${E2E_OUT:-$(dirname "$0")/out}
    mkdir -p "$OUT"
    rm -f "$OUT/novlan"
    if [ "$NO_VLAN" != 1 ] && ! vlan_supported; then
        echo "topo.sh: kernel has no 8021q support, using separate bridges instead of VLANs" >&2
        NO_VLAN=1
    fi
    [ "$NO_VLAN" = 1 ] && touch "$OUT/novlan"
    for n in $NODES pod sw; do
        ip netns add "$P-$n"
        nsx "$n" ip link set lo up
    done
    # management network in the root namespace
    ip link add "$P-mgmt" type bridge
    ip addr add 192.168.250.1/24 dev "$P-mgmt"
    ip link set "$P-mgmt" up
    for n in $NODES pod; do
        i=$(idx "$n")
        ip link add "m-$P-$n" type veth peer name mgmt0 netns "$P-$n"
        ip link set "m-$P-$n" master "$P-mgmt" up
        nsx "$n" ip addr add "192.168.250.$((i + 10 > 250 ? 250 : i + 10))/24" dev mgmt0
        nsx "$n" ip link set mgmt0 up
    done
    # switch namespace: access bridge for A, trunk bridge for B/C
    nsx sw ip link add brA type bridge
    nsx sw ip link add brT type bridge
    if [ "$NO_VLAN" = 1 ]; then
        nsx sw ip link add brB type bridge
        nsx sw ip link add brC type bridge
        nsx sw ip link set brB up
        nsx sw ip link set brC up
    fi
    nsx sw ip link set brA up
    nsx sw ip link set brT up
    for n in $NODES; do
        i=$(idx "$n")
        nsx sw ip link add "a-$n" type veth peer name eth0 netns "$P-$n"
        nsx sw ip link set "a-$n" master brA up
        nsx "$n" ip addr add "10.10.1.$i/24" dev eth0
        nsx "$n" ip link set eth0 up
        [ "$n" = rt ] && continue
        if [ "$NO_VLAN" = 1 ]; then
            nsx sw ip link add "b-$n" type veth peer name eth1.300 netns "$P-$n"
            nsx sw ip link set "b-$n" master brB up
            nsx sw ip link add "c-$n" type veth peer name eth1.301 netns "$P-$n"
            nsx sw ip link set "c-$n" master brC up
        else
            nsx sw ip link add "t-$n" type veth peer name eth1 netns "$P-$n"
            nsx sw ip link set "t-$n" master brT up
            nsx "$n" ip link set eth1 up
            nsx "$n" ip link add link eth1 name eth1.300 type vlan id 300
            nsx "$n" ip link add link eth1 name eth1.301 type vlan id 301
        fi
        nsx "$n" ip addr add "10.30.0.$i/24" dev eth1.300
        nsx "$n" ip addr add "10.31.0.$i/24" dev eth1.301
        nsx "$n" ip link set eth1.300 up
        nsx "$n" ip link set eth1.301 up
        nsx "$n" tc qdisc add dev eth1.300 root tbf rate 900mbit burst 256kb latency 50ms
        nsx "$n" tc qdisc add dev eth1.301 root tbf rate 100mbit burst 64kb latency 50ms
    done
    # MTU 1400 on the switch port of rt (segment A)
    nsx sw ip link set a-rt mtu 1400
    # macvlan: n3 keeps its address on the parent, the pod gets a macvlan child
    nsx n3 ip link add link eth1.301 name mv0 type macvlan mode bridge
    nsx n3 ip link set mv0 netns "$P-pod"
    nsx pod ip link set mv0 name eth0
    nsx pod ip addr add 10.31.0.13/24 dev eth0
    nsx pod ip link set eth0 up
    # transparent proxy on n2 in segment B: TCP to the test port is redirected, ICMP passes
    nsx n2 iptables -t nat -A PREROUTING -i eth1.300 -p tcp --dport 47700 -j REDIRECT --to-ports 47799
}

case ${1:-} in
up) up ;;
down) down ;;
*) echo "usage: $0 up|down" >&2; exit 2 ;;
esac
