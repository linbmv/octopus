package model

import "testing"

func TestUserLoginRequestedExpiryMinutes(t *testing.T) {
	explicit := 1440
	legacy := 60
	tests := []struct {
		name    string
		login   UserLogin
		want    int
		wantErr bool
	}{
		{name: "default", login: UserLogin{}, want: 0},
		{name: "explicit minutes", login: UserLogin{ExpiresInMinutes: &explicit}, want: explicit},
		{name: "legacy minutes", login: UserLogin{LegacyExpire: &legacy}, want: legacy},
		{name: "ambiguous fields", login: UserLogin{ExpiresInMinutes: &explicit, LegacyExpire: &legacy}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.login.RequestedExpiryMinutes()
			if test.wantErr {
				if err == nil {
					t.Fatal("RequestedExpiryMinutes() error = nil")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("RequestedExpiryMinutes() = %d, %v; want %d, nil", got, err, test.want)
			}
		})
	}
}

func TestUserLoginRequestedAuthMode(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "compatibility default", want: UserAuthModeBearer},
		{name: "explicit bearer", value: UserAuthModeBearer, want: UserAuthModeBearer},
		{name: "browser cookie", value: UserAuthModeCookie, want: UserAuthModeCookie},
		{name: "invalid", value: "query", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := (UserLogin{AuthMode: test.value}).RequestedAuthMode()
			if test.wantErr {
				if err == nil {
					t.Fatal("RequestedAuthMode() error = nil")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("RequestedAuthMode() = %q, %v; want %q, nil", got, err, test.want)
			}
		})
	}
}
