package main

import "testing"

// TestDeriveBPF 覆盖端口派生协议（tcp/udp/both）与显式 BPF 覆盖语义。
func TestDeriveBPF(t *testing.T) {
	cases := []struct {
		name string
		p    CaptureParams
		want string
	}{
		{name: "empty captures all", p: CaptureParams{}, want: ""},
		{name: "explicit bpf wins", p: CaptureParams{BPF: "udp", Ports: []int32{8080}}, want: "udp"},
		{name: "port default tcp", p: CaptureParams{Ports: []int32{8080}}, want: "tcp port 8080"},
		{name: "port tcp", p: CaptureParams{Ports: []int32{8080}, Protocol: "tcp"}, want: "tcp port 8080"},
		{name: "port udp", p: CaptureParams{Ports: []int32{8080}, Protocol: "udp"}, want: "udp port 8080"},
		{name: "port both", p: CaptureParams{Ports: []int32{8080}, Protocol: "both"}, want: "tcp port 8080 or udp port 8080"},
		{name: "protocol case-insensitive", p: CaptureParams{Ports: []int32{53}, Protocol: "UDP"}, want: "udp port 53"},
		{name: "invalid protocol falls back tcp", p: CaptureParams{Ports: []int32{8080}, Protocol: "sctp"}, want: "tcp port 8080"},
		{name: "multiple ports", p: CaptureParams{Ports: []int32{8080, 443}, Protocol: "both"}, want: "tcp port 8080 or udp port 8080 or tcp port 443 or udp port 443"},
		{name: "hosts combined with or", p: CaptureParams{Ports: []int32{8080}, Hosts: []string{"api.x.com"}}, want: "tcp port 8080 or host api.x.com"},
		{name: "invalid port skipped", p: CaptureParams{Ports: []int32{0, 70000, 53}, Protocol: "udp"}, want: "udp port 53"},
		{name: "udp with hosts", p: CaptureParams{Ports: []int32{53}, Hosts: []string{"dns.x.com"}, Protocol: "udp"}, want: "udp port 53 or host dns.x.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveBPF(tc.p); got != tc.want {
				t.Errorf("deriveBPF(%+v) = %q, want %q", tc.p, got, tc.want)
			}
		})
	}
}
