//go:build !js

package mintcore

import (
	"net/http"
	"testing"
)

func TestMintHTTP_DefaultClient(t *testing.T) {
	doer := mintHTTP()
	if doer == nil {
		t.Fatal("mintHTTP() returned nil")
	}
	client, ok := doer.(*http.Client)
	if !ok {
		t.Fatalf("expected *http.Client, got %T", doer)
	}
	if client.Timeout == 0 {
		t.Fatal("expected non-zero timeout on default client")
	}
}

func TestSetHTTPDoerForTest(t *testing.T) {
	fake := &fakeHTTPDoer{}
	SetHTTPDoerForTest(t, fake)

	doer := mintHTTP()
	if doer != fake {
		t.Fatal("expected mintHTTP to return the test override")
	}
}

func TestSetHTTPDoerForTest_RestoresOnCleanup(t *testing.T) {
	original := httpDoerOverride

	inner := func(t *testing.T) {
		fake := &fakeHTTPDoer{}
		SetHTTPDoerForTest(t, fake)
		if mintHTTP() != fake {
			t.Fatal("expected override during test")
		}
	}
	t.Run("inner", inner)

	if httpDoerOverride != original {
		t.Fatal("expected httpDoerOverride to be restored after cleanup")
	}
}
