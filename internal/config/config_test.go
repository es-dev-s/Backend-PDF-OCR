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

func TestCapWorkers(t *testing.T) {
	if got := capWorkers(16, true); got != 2 {
		t.Fatalf("hosted 16 -> %d want 2", got)
	}
	if got := capWorkers(1, true); got != 1 {
		t.Fatalf("hosted 1 -> %d want 1", got)
	}
	if got := capWorkers(16, false); got != 4 {
		t.Fatalf("local 16 -> %d want 4", got)
	}
	if got := capWorkers(0, false); got != 2 {
		t.Fatalf("zero -> %d want 2", got)
	}
}

func TestCapHeavy(t *testing.T) {
	if got := capHeavy(0, 2, true); got != 2 {
		t.Fatalf("hosted default -> %d want 2", got)
	}
	if got := capHeavy(64, 2, true); got != 2 {
		t.Fatalf("hosted override -> %d want 2", got)
	}
	if got := capHeavy(0, 4, false); got != 2 {
		t.Fatalf("local default -> %d want 2", got)
	}
	if got := capHeavy(64, 4, false); got != 8 {
		t.Fatalf("local override -> %d want 8", got)
	}
}

func TestCapTitle(t *testing.T) {
	// Engine calls wait on the network, so they run wider than fingerprinting.
	if got := capTitle(0, 2, true); got != 2 {
		t.Fatalf("hosted default -> %d want 2", got)
	}
	if got := capTitle(64, 2, true); got != 4 {
		t.Fatalf("hosted override -> %d want 4", got)
	}
	if got := capTitle(0, 4, false); got != 4 {
		t.Fatalf("local default -> %d want 4", got)
	}
	if got := capTitle(-1, 0, false); got != 1 {
		t.Fatalf("degenerate -> %d want 1", got)
	}
}

func TestNormalizeEngineURL(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "http://127.0.0.1:8000"},
		{"http://127.0.0.1:8000", "http://127.0.0.1:8000"},
		{"http://127.0.0.1:8000/", "http://127.0.0.1:8000"},
		{"127.0.0.1:8000", "http://127.0.0.1:8000"},
		{"localhost:8000", "http://localhost:8000"},
		{"text-extracter-v2-production.up.railway.app", "https://text-extracter-v2-production.up.railway.app"},
		{"https://text-extracter-v2-production.up.railway.app/", "https://text-extracter-v2-production.up.railway.app"},
	}
	for _, tt := range tests {
		if got := normalizeEngineURL(tt.in); got != tt.want {
			t.Fatalf("%q -> %q want %q", tt.in, got, tt.want)
		}
	}
}
