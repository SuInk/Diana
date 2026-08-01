package assistant

import "testing"

func TestNormalizePlatformIDMigratesLegacyNames(t *testing.T) {
	cases := map[string]string{
		"":                    PlatformNapCat,
		"NapCat / OneBot V11": PlatformNapCat,
		"Lagrange.Core":       PlatformLagrange,
		"go-cqhttp":           PlatformGoCQHTTP,
	}
	for input, want := range cases {
		if got := NormalizePlatformID(input); got != want {
			t.Fatalf("NormalizePlatformID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidatePlatformRejectsUnknownAdapter(t *testing.T) {
	if err := ValidatePlatform(PlatformLagrange); err != nil {
		t.Fatalf("ValidatePlatform(%q) error = %v", PlatformLagrange, err)
	}
	if err := ValidatePlatform("telegram"); err == nil {
		t.Fatal("ValidatePlatform(telegram) unexpectedly succeeded")
	}
}
