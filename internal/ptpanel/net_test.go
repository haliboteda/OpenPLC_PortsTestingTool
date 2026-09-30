package ptpanel

import "testing"

// Ping's output is the one thing here that is parsed out of prose, and prose
// changes with the language the machine speaks. These are real captures.
func TestReplyTimesCountsRepliesNotTheSummary(t *testing.T) {
	// Windows, Chinese. *** The summary at the bottom carries three more times
	// *** - this is what made four pings report as seven on 2026-09-14.
	zh := "正在 Ping 192.168.0.3 具有 32 字节的数据:\r\n" +
		"来自 192.168.0.3 的回复: 字节=32 时间=5ms TTL=255\r\n" +
		"来自 192.168.0.3 的回复: 字节=32 时间=2ms TTL=255\r\n" +
		"\r\n192.168.0.3 的 Ping 统计信息:\r\n" +
		"    数据包: 已发送 = 2，已接收 = 2，丢失 = 0 (0% 丢失)，\r\n" +
		"往返行程的估计时间(以毫秒为单位):\r\n" +
		"    最短 = 2ms，最长 = 5ms，平均 = 3ms\r\n"

	got := replyTimes(zh)
	if len(got) != 2 {
		t.Errorf("Chinese Windows: %d replies, want 2 - %v", len(got), got)
	}

	en := "Pinging 192.168.0.3 with 32 bytes of data:\r\n" +
		"Reply from 192.168.0.3: bytes=32 time=1ms TTL=255\r\n" +
		"Reply from 192.168.0.3: bytes=32 time<1ms TTL=255\r\n" +
		"\r\nPing statistics for 192.168.0.3:\r\n" +
		"    Minimum = 0ms, Maximum = 1ms, Average = 0ms\r\n"
	got = replyTimes(en)
	// "time<1ms" carries no "=" before the number, so only the first line has a
	// time this can read. It is still one reply and not two - undercounting is
	// the safe direction here, and the raw output goes back for the rest.
	if len(got) != 1 {
		t.Errorf("English Windows: %d replies, want 1 - %v", len(got), got)
	}

	linux := "PING 192.168.0.3 (192.168.0.3) 56(84) bytes of data.\n" +
		"64 bytes from 192.168.0.3: icmp_seq=1 ttl=255 time=0.512 ms\n" +
		"64 bytes from 192.168.0.3: icmp_seq=2 ttl=255 time=1.043 ms\n" +
		"\n--- 192.168.0.3 ping statistics ---\n" +
		"2 packets transmitted, 2 received, 0% packet loss, time 1001ms\n" +
		"rtt min/avg/max/mdev = 0.512/0.777/1.043/0.265 ms\n"
	got = replyTimes(linux)
	if len(got) != 2 {
		t.Fatalf("Linux: %d replies, want 2 - %v", len(got), got)
	}
	if got[0] != 0.512 || got[1] != 1.043 {
		t.Errorf("Linux times = %v, want [0.512 1.043] - fractions must survive", got)
	}
}

// Nothing came back, so nothing may be reported as having come back. A host
// that is simply not there is the most common real answer.
func TestReplyTimesOnNoReply(t *testing.T) {
	dead := "正在 Ping 192.168.0.99 具有 32 字节的数据:\r\n" +
		"请求超时。\r\n请求超时。\r\n" +
		"    数据包: 已发送 = 2，已接收 = 0，丢失 = 2 (100% 丢失)，\r\n"
	if got := replyTimes(dead); len(got) != 0 {
		t.Errorf("got %v replies from a host that answered none", got)
	}
}

// Same subnet is the whole question: it decides whether the two can talk
// without a router, and a bench that needs a router is a bench nobody debugged.
func TestLocalNICsMarksTheReachableOne(t *testing.T) {
	// Against this machine's own addresses rather than a fixture, because what
	// is being checked is that the subnet maths is applied at all - a version
	// that marked everything, or nothing, would look the same in a fixture.
	all := localNICs("")
	for _, n := range all {
		if n.Same {
			t.Errorf("%s marked reachable with no board address given", n.IP)
		}
	}

	if len(all) == 0 {
		t.Skip("this machine has no non-loopback IPv4 address")
	}
	// A board one address along from a real NIC has to match that NIC, and an
	// address in a range nobody is on has to match none.
	pick := all[0]
	if hits := localNICs(pick.IP); len(hits) > 0 {
		found := false
		for _, n := range hits {
			if n.IP == pick.IP && n.Same {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not consider itself on its own subnet", pick.IP)
		}
	}

	for _, n := range localNICs("203.0.113.7") { // TEST-NET-3, nobody routes it
		if n.Same {
			t.Errorf("%s claims to share a subnet with 203.0.113.7", n.CIDR)
		}
	}
}
