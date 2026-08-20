package docker

import "testing"

func TestParseIPAddrJSON(t *testing.T) {
	out := `[
  {"ifindex":1,"ifname":"lo","mtu":65536,"addr_info":[{"family":"inet","local":"127.0.0.1","prefixlen":8}]},
  {"ifindex":2,"ifname":"eth0","mtu":1500,"addr_info":[{"family":"inet","local":"172.17.0.5","prefixlen":16},{"family":"inet6","local":"fe80::1","prefixlen":64}]}
]`
	ifaces, err := parseIPAddrJSON(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(ifaces) != 2 {
		t.Fatalf("got %d interfaces, want 2: %+v", len(ifaces), ifaces)
	}
	eth0 := findIface(t, ifaces, "eth0")
	if eth0.MTU != 1500 {
		t.Errorf("eth0 MTU = %d, want 1500", eth0.MTU)
	}
	if len(eth0.Subnets) != 2 || eth0.Subnets[0] != "172.17.0.5/16" {
		t.Errorf("eth0 subnets = %v", eth0.Subnets)
	}
}

func TestParseIPAddrText(t *testing.T) {
	out := `1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 qdisc noqueue state UNKNOWN group default qlen 1000
    link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00
    inet 127.0.0.1/8 scope host lo
       valid_lft forever preferred_lft forever
2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP group default qlen 1000
    link/ether 02:42:ac:11:00:05 brd ff:ff:ff:ff:ff:ff
    inet 172.17.0.5/16 brd 172.17.255.255 scope global eth0
       valid_lft forever preferred_lft forever
`
	ifaces, err := parseIPAddrText(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(ifaces) != 2 {
		t.Fatalf("got %d interfaces, want 2: %+v", len(ifaces), ifaces)
	}
	eth0 := findIface(t, ifaces, "eth0")
	if eth0.MTU != 1500 || len(eth0.Subnets) != 1 || eth0.Subnets[0] != "172.17.0.5/16" {
		t.Errorf("eth0 = %+v", eth0)
	}
}

func TestParseIfconfigText_Linux(t *testing.T) {
	out := `eth0: flags=4163<UP,BROADCAST,RUNNING,MULTICAST>  mtu 1500
        inet 172.17.0.5  netmask 255.255.0.0  broadcast 172.17.255.255
        ether 02:42:ac:11:00:05  txqueuelen 0  (Ethernet)
`
	ifaces, err := parseIfconfigText(out)
	if err != nil {
		t.Fatal(err)
	}
	eth0 := findIface(t, ifaces, "eth0")
	if eth0.MTU != 1500 || len(eth0.Subnets) != 1 || eth0.Subnets[0] != "172.17.0.5/16" {
		t.Errorf("eth0 = %+v", eth0)
	}
}

func TestParseIfconfigText_MacOSHexNetmask(t *testing.T) {
	out := `en0: flags=8863<UP,BROADCAST,SMART,RUNNING,SIMPLEX,MULTICAST> mtu 1500
	ether ac:de:48:00:11:22
	inet6 fe80::1%en0 prefixlen 64 secured scopeid 0x4
	inet 192.168.1.5 netmask 0xffffff00 broadcast 192.168.1.255
	media: autoselect
`
	ifaces, err := parseIfconfigText(out)
	if err != nil {
		t.Fatal(err)
	}
	en0 := findIface(t, ifaces, "en0")
	if en0.MTU != 1500 || len(en0.Subnets) != 1 || en0.Subnets[0] != "192.168.1.5/24" {
		t.Errorf("en0 = %+v", en0)
	}
}

func TestParseIpconfigText(t *testing.T) {
	out := `Windows IP Configuration


Ethernet adapter Ethernet:

   Connection-specific DNS Suffix  . :
   IPv4 Address. . . . . . . . . . . : 192.168.1.100
   Subnet Mask . . . . . . . . . . . : 255.255.255.0
   Default Gateway . . . . . . . . . : 192.168.1.1
`
	ifaces, err := parseIpconfigText(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(ifaces) != 1 {
		t.Fatalf("got %d interfaces, want 1: %+v", len(ifaces), ifaces)
	}
	if ifaces[0].Subnets[0] != "192.168.1.100/24" {
		t.Errorf("subnet = %v", ifaces[0].Subnets)
	}
	// A non-empty Default Gateway is ipconfig's only signal for "this is the
	// interface the host actually routes through" — there's no separate route
	// command to fall back on for Windows the way there is for Linux/macOS.
	if !ifaces[0].IsDefault {
		t.Error("interface with a Default Gateway should be marked IsDefault")
	}
}

func TestParseIpconfigText_MultipleAdaptersOnlyOneHasAGateway(t *testing.T) {
	out := `Windows IP Configuration


Ethernet adapter vEthernet (Docker):

   IPv4 Address. . . . . . . . . . . : 172.20.0.1
   Subnet Mask . . . . . . . . . . . : 255.255.240.0
   Default Gateway . . . . . . . . . :

Wireless LAN adapter Wi-Fi:

   IPv4 Address. . . . . . . . . . . : 192.168.1.100
   Subnet Mask . . . . . . . . . . . : 255.255.255.0
   Default Gateway . . . . . . . . . : 192.168.1.1
`
	ifaces, err := parseIpconfigText(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(ifaces) != 2 {
		t.Fatalf("got %d interfaces, want 2: %+v", len(ifaces), ifaces)
	}
	docker0 := findIface(t, ifaces, "vEthernet (Docker)")
	if docker0.IsDefault {
		t.Error("the adapter with an EMPTY Default Gateway must not be marked default")
	}
	wifi := findIface(t, ifaces, "Wi-Fi")
	if !wifi.IsDefault {
		t.Error("the adapter with a real Default Gateway should be marked default")
	}
}

func TestParseDefaultRouteIface(t *testing.T) {
	if got := parseDefaultRouteIface("default via 172.17.0.1 dev eth0 \n"); got != "eth0" {
		t.Errorf("got %q, want eth0", got)
	}
	if got := parseDefaultRouteIface("garbage, no route here"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestParseRouteGetDefaultIface(t *testing.T) {
	out := `   route to: default
destination: default
       mask: default
    gateway: 192.168.1.1
  interface: en0
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING>
`
	if got := parseRouteGetDefaultIface(out); got != "en0" {
		t.Errorf("got %q, want en0", got)
	}
	if got := parseRouteGetDefaultIface("garbage, no route here"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestNetmaskToPrefixLen(t *testing.T) {
	cases := map[string]int{
		"255.255.255.0": 24,
		"255.255.0.0":   16,
		"255.0.0.0":     8,
		"0xffffff00":    24,
		"0xffff0000":    16,
	}
	for mask, want := range cases {
		got, ok := netmaskToPrefixLen(mask)
		if !ok || got != want {
			t.Errorf("netmaskToPrefixLen(%q) = %d,%v want %d,true", mask, got, ok, want)
		}
	}
	if _, ok := netmaskToPrefixLen("not-a-mask"); ok {
		t.Error("garbage mask should not parse")
	}
}

func TestParseDFOutput(t *testing.T) {
	out := "Filesystem     1024-blocks     Used Available Capacity Mounted on\n/dev/sda1        41251136 12345678  28800000      31% /\n"
	total, free, ok := parseDFOutput(out)
	if !ok {
		t.Fatal("expected a parse")
	}
	if total != 41251136*1024 || free != 28800000*1024 {
		t.Errorf("total=%d free=%d", total, free)
	}
}

func TestParseWMICOutput(t *testing.T) {
	out := "\r\n\r\nFreeSpace=53687091200\r\nSize=214748364800\r\n\r\n\r\n"
	total, free, ok := parseWMICOutput(out)
	if !ok {
		t.Fatal("expected a parse")
	}
	if total != 214748364800 || free != 53687091200 {
		t.Errorf("total=%d free=%d", total, free)
	}
}

func findIface(t *testing.T, ifaces []hostIface, name string) hostIface {
	t.Helper()
	for _, hi := range ifaces {
		if hi.Name == name {
			return hi
		}
	}
	t.Fatalf("interface %q not found in %+v", name, ifaces)
	return hostIface{}
}
