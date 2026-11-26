package panel

import (
	"os"
	"testing"
)

var openedURLs []string

func TestMain(m *testing.M) {
	openURL = func(url string) error {
		openedURLs = append(openedURLs, url)
		return nil
	}
	os.Exit(m.Run())
}
