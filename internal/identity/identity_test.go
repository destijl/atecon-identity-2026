package identity

import "testing"

func TestAtespaceForEmail(t *testing.T) {
	tests := []struct {
		email, want string
		wantErr     bool
	}{
		{email: "alice@gmail.com", want: "alice-gmail-com"},
		{email: "bob@gmail.com", want: "bob-gmail-com"},
		{email: "a.b+tag@example.co.uk", want: "a-b-tag-example-co-uk"},
		{email: "Alice@gmail.com", wantErr: true},
		{email: "alice", wantErr: true},
		{email: "", wantErr: true},
		{email: "alice@gmail.com/../bob", wantErr: true},
		{email: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa@gmail.com", wantErr: true},
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
		{id: "spiffe://substrate-actor.local/actor/alice-gmail-com/email-agent", want: Actor{"alice-gmail-com", "email-agent"}},
		{id: "spiffe://evil.example/actor/alice-gmail-com/email-agent", wantErr: true},
		{id: "spiffe://substrate-actor.local/ateom-for-actor/alice-gmail-com/email-agent", wantErr: true},
		{id: "spiffe://substrate-actor.local/actor/alice-gmail-com", wantErr: true},
		{id: "spiffe://substrate-actor.local/actor/alice-gmail-com/a/b", wantErr: true},
		{id: "spiffe://substrate-actor.local/actor/../email-agent", wantErr: true},
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

func TestFromJWTSubject(t *testing.T) {
	if got, err := FromJWTSubject("actor/alice-gmail-com/email-agent"); err != nil || got != (Actor{"alice-gmail-com", "email-agent"}) {
		t.Errorf("FromJWTSubject() = %v, %v", got, err)
	}
	for _, bad := range []string{"worker-for-actor/a/b", "actor/a", "", "actor/a/b/c"} {
		if _, err := FromJWTSubject(bad); err == nil {
			t.Errorf("FromJWTSubject(%q) succeeded, want error", bad)
		}
	}
}
