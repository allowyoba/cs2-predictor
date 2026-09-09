//go:build live

package valvevrs

import (
	"context"
	"fmt"
	"testing"
)

func TestZZZLiveCheck_RealGitHub(t *testing.T) {
	p := NewProvider(DefaultConfig(), nil)
	ranked, err := p.FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("fetched %d ranked teams\n", len(ranked))
	for i, r := range ranked {
		if i >= 5 {
			break
		}
		fmt.Printf("%+v\n", r)
	}
}
