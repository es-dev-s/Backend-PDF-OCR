package blob

import "testing"

func TestR2Ready(t *testing.T) {
	if r2Ready(R2Options{}) {
		t.Fatal("empty creds must not look ready")
	}
	if !r2Ready(R2Options{
		AccountID: "a",
		AccessKey: "b",
		Secret:    "c",
		Bucket:    "d",
	}) {
		t.Fatal("complete creds must look ready")
	}
}
