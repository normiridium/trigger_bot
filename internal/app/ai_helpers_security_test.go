package app

import "testing"

func TestValidateExternalImageURL(t *testing.T) {
	t.Run("allow public ip", func(t *testing.T) {
		if _, err := validateExternalImageURL("https://1.1.1.1/test.png"); err != nil {
			t.Fatalf("expected public ip url to pass, got err=%v", err)
		}
	})

	t.Run("block localhost host", func(t *testing.T) {
		if _, err := validateExternalImageURL("http://localhost/a.png"); err == nil {
			t.Fatalf("expected localhost to be blocked")
		}
	})

	t.Run("block private ipv4", func(t *testing.T) {
		if _, err := validateExternalImageURL("http://10.0.0.1/a.png"); err == nil {
			t.Fatalf("expected private ipv4 to be blocked")
		}
	})

	t.Run("block loopback ipv6", func(t *testing.T) {
		if _, err := validateExternalImageURL("http://[::1]/a.png"); err == nil {
			t.Fatalf("expected loopback ipv6 to be blocked")
		}
	})

	t.Run("block unsupported scheme", func(t *testing.T) {
		if _, err := validateExternalImageURL("file:///tmp/a.png"); err == nil {
			t.Fatalf("expected file scheme to be blocked")
		}
	})
}
