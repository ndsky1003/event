package msg

import (
	"testing"
)

func TestMsgTypeString(t *testing.T) {
	tests := []struct {
		in   MsgType
		want string
	}{
		{MsgType_invalid, "invalid"},
		{MsgType_ping, "Ping"},
		{MsgType_pong, "Pong"},
		{MsgType_varify, "varify"},
		{MsgType_prepared, "Prepared"},
		{MsgType_on, "on"},
		{MsgType_req, "req"},
		{MsgType_res, "res"},
		{MsgType(999), "未知:(999)"},
	}
	for _, tt := range tests {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("MsgType(%d).String() = %q, want %q", int(tt.in), got, tt.want)
		}
	}
}

func TestEventTopicMatch(t *testing.T) {
	plain := NewEventTopic("db_op")
	if !plain.Match("db_op") {
		t.Errorf("plain topic should match exact event")
	}
	if plain.Match("db_other") {
		t.Errorf("plain topic should not match different event")
	}
	if !plain.Equal("db_op") {
		t.Errorf("plain topic Equal should be true for same event")
	}
	if plain.IsRegexp {
		t.Errorf("plain topic should not be regexp")
	}
	if plain.GetEventType() != "db_op" {
		t.Errorf("GetEventType mismatch")
	}

	reg := NewEventTopic("/db_[a-z]+/")
	if !reg.IsRegexp {
		t.Errorf("regexp topic should be regexp")
	}
	if !reg.Match("db_insert") {
		t.Errorf("regexp topic should match db_insert")
	}
	if !reg.Match("db_delete") {
		t.Errorf("regexp topic should match db_delete")
	}
	if reg.Match("db_123") {
		t.Errorf("regexp topic should not match db_123")
	}
	if reg.Equal("db_insert") {
		t.Errorf("regexp topic Equal should be false for different literal")
	}
}

func TestEventTopicFindStringSubmatch(t *testing.T) {
	reg := NewEventTopic("/db_([a-z]+)/")
	got := reg.FindStringSubmatch("db_insert")
	if len(got) != 2 || got[0] != "db_insert" || got[1] != "insert" {
		t.Errorf("FindStringSubmatch = %v", got)
	}

	plain := NewEventTopic("db_op")
	gotPlain := plain.FindStringSubmatch("db_op")
	if len(gotPlain) != 1 || gotPlain[0] != "db_op" {
		t.Errorf("plain FindStringSubmatch = %v", gotPlain)
	}
}

func TestMsgString(t *testing.T) {
	if got := (*Msg)(nil).String(); got != "" {
		t.Errorf("nil Msg.String() should be empty, got %q", got)
	}
	m := &Msg{T: MsgType_req, ServerSeq: 1, ClientSeq: 2, Name: "n", EventType: "et", BodyCount: 3, Error: "e"}
	if got := m.String(); got == "" {
		t.Errorf("Msg.String() should not be empty")
	}
}

func TestNewCall(t *testing.T) {
	if got := NewCall(nil); got != nil {
		t.Errorf("NewCall(nil) should be nil")
	}
	m := &Msg{T: MsgType_req}
	c := NewCall(m)
	if c == nil || c.Msg != m || c.Done == nil {
		t.Errorf("NewCall returned invalid call")
	}
}
