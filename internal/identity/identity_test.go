package identity

import "testing"

func TestAtespaceForEmail(t *testing.T) {
	tests := []struct {
		email, want string
		wantErr     bool
	}{
		{email: "alice@gmail.com", want: "u-alice"},
		{email: "bob@gmail.com", want: "u-bob"},
		{email: "a.b+tag@example.co.uk", want: "u-a-b-tag"},
		{email: "alice.@gmail.com", want: "u-alice"},
		{email: "Alice@gmail.com", wantErr: true},
		{email: "alice", wantErr: true},
		{email: "", wantErr: true},
		{email: "alice@gmail.com/../bob", wantErr: true},
		{email: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa@gmail.com", wantErr: true},
	}
	for _, tt := range tests {
		got, err := AtespaceForEmail(tt.email)
		if (err != nil) != tt.wantErr {
			t.Errorf("AtespaceForEmail(%q) err = %v, wantErr %v", tt.email, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("AtespaceForEmail(%q) = %q, want %q", tt.email, got, tt.want)
		}
	}
}

func TestFromSPIFFEID(t *testing.T) {
	tests := []struct {
		id      string
		want    Actor
		wantErr bool
	}{
		{id: "spiffe://substrate-actor.local/actor/u-alice/mail-agent", want: Actor{"u-alice", "mail-agent"}},
		{id: "spiffe://evil.example/actor/u-alice/mail-agent", wantErr: true},
		{id: "spiffe://substrate-actor.local/ateom-for-actor/u-alice/mail-agent", wantErr: true},
		{id: "spiffe://substrate-actor.local/actor/u-alice", wantErr: true},
		{id: "spiffe://substrate-actor.local/actor/u-alice/a/b", wantErr: true},
		{id: "spiffe://substrate-actor.local/actor/../mail-agent", wantErr: true},
		{id: "spiffe://substrate-actor.local/actor/Alice/x", wantErr: true},
		{id: "https://substrate-actor.local/actor/a/b", wantErr: true},
		{id: "spiffe://substrate-actor.local/actor/a/b?x=1", wantErr: true},
	}
	for _, tt := range tests {
		got, err := FromSPIFFEID(tt.id)
		if (err != nil) != tt.wantErr {
			t.Errorf("FromSPIFFEID(%q) err = %v, wantErr %v", tt.id, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("FromSPIFFEID(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}
