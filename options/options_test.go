package options

import (
	"testing"
	"time"
)

func TestClientOptionsSetAndMerge(t *testing.T) {
	name1 := "client1"
	name2 := "client2"
	interval1 := 1 * time.Second
	interval2 := 2 * time.Second

	opt := Client().
		SetName(name1).
		SetCheckInterval(interval1).
		Merge(
			Client().SetName(name2),
			Client().SetCheckInterval(interval2),
		)

	if opt.Name == nil || *opt.Name != name2 {
		t.Errorf("Name = %v, want %q", opt.Name, name2)
	}
	if opt.CheckInterval == nil || *opt.CheckInterval != interval2 {
		t.Errorf("CheckInterval = %v, want %v", opt.CheckInterval, interval2)
	}
}

func TestClientOptionsMergeNilSafe(t *testing.T) {
	opt := Client()
	opt.Merge()
	if opt.Name != nil || opt.CodecFunc != nil || opt.CheckInterval != nil {
		t.Errorf("Merge() with no args should not modify options")
	}
}

func TestClientOptionsNilReceiver(t *testing.T) {
	var opt *ClientOptions
	if got := opt.SetName("x"); got != nil {
		t.Errorf("nil receiver SetName should return nil")
	}
	if got := opt.SetCheckInterval(time.Second); got != nil {
		t.Errorf("nil receiver SetCheckInterval should return nil")
	}
	if got := opt.SetHeartInterval(time.Second); got != nil {
		t.Errorf("nil receiver SetHeartInterval should return nil")
	}
	if got := opt.SetIsStopHeart(true); got != nil {
		t.Errorf("nil receiver SetIsStopHeart should return nil")
	}
}

func TestServerOptionsSetAndMerge(t *testing.T) {
	timeout1 := 1 * time.Second
	timeout2 := 2 * time.Second

	opt := Server().
		SetReqTimeout(timeout1).
		Merge(Server().SetReqTimeout(timeout2))

	if opt.ReqTimeout == nil || *opt.ReqTimeout != timeout2 {
		t.Errorf("ReqTimeout = %v, want %v", opt.ReqTimeout, timeout2)
	}
}

func TestServerOptionsMergeNilSafe(t *testing.T) {
	opt := Server()
	opt.Merge()
	if opt.ReqTimeout != nil || opt.CodecFunc != nil {
		t.Errorf("Merge() with no args should not modify options")
	}
}
