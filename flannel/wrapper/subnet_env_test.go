package main

import "testing"

func TestApplyFlannelSubnetEnv(t *testing.T) {
	t.Setenv("SUBNET_LEN", "")
	t.Setenv("SUBNET_MIN", "")
	t.Setenv("SUBNET_MAX", "")
	cfg := &Config{}
	if err := applyFlannelSubnetEnv(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.SubnetLen != 0 || cfg.SubnetMin != "" || cfg.SubnetMax != "" {
		t.Fatalf("empty env changed config: %#v", cfg)
	}

	cfg.SubnetLen = 20
	t.Setenv("SUBNET_LEN", "24")
	t.Setenv("SUBNET_MIN", "100.64.1.0")
	t.Setenv("SUBNET_MAX", "100.127.255.0")
	if err := applyFlannelSubnetEnv(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.SubnetLen != 20 || cfg.SubnetMin != "100.64.1.0" || cfg.SubnetMax != "100.127.255.0" {
		t.Fatalf("flag must win and empty bounds must fill: %#v", cfg)
	}

	cfg = &Config{}
	if err := applyFlannelSubnetEnv(cfg); err != nil || cfg.SubnetLen != 24 {
		t.Fatalf("env len: %d %v", cfg.SubnetLen, err)
	}
	t.Setenv("SUBNET_LEN", "nope")
	cfg.SubnetLen = 0
	if err := applyFlannelSubnetEnv(cfg); err == nil {
		t.Fatal("invalid SUBNET_LEN must fail")
	}
}
