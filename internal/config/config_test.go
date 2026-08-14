package config

import "testing"

func TestPickStorage(t *testing.T) {
	tests := []struct {
		name    string
		driver  string
		r2OK    bool
		hosted  bool
		want    string
		wantErr bool
	}{
		{name: "r2 creds win over local", driver: "local", r2OK: true, want: "r2"},
		{name: "r2 creds win when unset", driver: "", r2OK: true, want: "r2"},
		{name: "hosted requires r2", driver: "local", hosted: true, wantErr: true},
		{name: "explicit r2 needs creds", driver: "r2", wantErr: true},
		{name: "laptop without r2 stays local", driver: "local", want: "local"},
		{name: "laptop unset without r2 stays local", driver: "", want: "local"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pickStorage(tt.driver, tt.r2OK, tt.hosted)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
