package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"steamcmd-serve/internal/mods/nexus"
)

const fakeKey = "test-key-not-real"

type fakeNexus struct {
	key  string
	acct nexus.Account
	err  error
}

func (f *fakeNexus) Set(_ context.Context, key string) (nexus.Account, error) {
	if f.err != nil {
		return nexus.Account{}, f.err
	}
	f.key = key
	return nexus.Account{HasValue: true, Premium: true, Username: "tester"}, nil
}
func (f *fakeNexus) Status(context.Context) (nexus.Account, error) {
	if f.key == "" {
		return nexus.Account{}, f.err
	}
	return nexus.Account{HasValue: true, Premium: true, Username: "tester"}, f.err
}
func (f *fakeNexus) Delete(context.Context) error { f.key = ""; return f.err }

func TestNexusKeyRoutes(t *testing.T) {
	n := &fakeNexus{}
	e := newEnvNexus(t, n)

	rr := e.do("GET", "/settings/nexus-api-key", "")
	if rr.Code != 200 || strings.TrimSpace(rr.Body.String()) != `{"hasValue":false}` {
		t.Fatalf("unset: %d %s", rr.Code, rr.Body)
	}

	rr = e.do("PUT", "/settings/nexus-api-key", `{"apiKey":"`+fakeKey+`"}`)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"premium":true`) || !strings.Contains(rr.Body.String(), `"username":"tester"`) {
		t.Fatalf("put: %d %s", rr.Code, rr.Body)
	}
	if n.key != fakeKey {
		t.Fatal("key not passed to NexusSettings")
	}
	rr2 := e.do("GET", "/settings/nexus-api-key", "")
	if rr2.Code != 200 || !strings.Contains(rr2.Body.String(), `"hasValue":true`) {
		t.Fatalf("get: %d %s", rr2.Code, rr2.Body)
	}
	for _, r := range []string{rr.Body.String(), rr2.Body.String()} {
		if strings.Contains(r, fakeKey) {
			t.Fatal("response contains the API key")
		}
	}

	if rr := e.do("DELETE", "/settings/nexus-api-key", ""); rr.Code != 204 {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body)
	}
	if n.key != "" {
		t.Fatal("not deleted")
	}
}

func TestNexusKeyErrors(t *testing.T) {
	n := &fakeNexus{err: nexus.ErrInvalidKey}
	e := newEnvNexus(t, n)
	rr := e.do("PUT", "/settings/nexus-api-key", `{"apiKey":"`+fakeKey+`"}`)
	if eb := decodeErr(t, rr); rr.Code != 400 || eb.Code != "invalid" || eb.Message != "NexusMods rejected this API key" {
		t.Fatalf("invalid key: %d %s", rr.Code, rr.Body)
	}
	if strings.Contains(rr.Body.String(), fakeKey) {
		t.Fatal("error echoes key")
	}

	n.err = errors.New("db exploded with " + "internal details")
	rr = e.serve("PUT", "/settings/nexus-api-key", `{"apiKey":"`+fakeKey+`"}`) // 500 is not in the spec
	if rr.Code != 500 || strings.Contains(rr.Body.String(), "exploded") {
		t.Fatalf("internal: %d %s", rr.Code, rr.Body)
	}

	n.err = nil
	for _, body := range []string{
		``, `{}`, `{"apiKey":""}`, `{"apiKey":"` + strings.Repeat("a", 201) + `"}`,
		`{"apiKey":"x","extra":1}`, `{"apiKey":5}`, `not json`,
	} {
		rr := e.do("PUT", "/settings/nexus-api-key", body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body %.30q: %d %s", body, rr.Code, rr.Body)
		}
	}
}

func TestNexusKeyUnwired(t *testing.T) {
	e := newEnv(t)
	for _, m := range []string{"PUT", "GET", "DELETE"} {
		rr := e.serve(m, "/settings/nexus-api-key", `{}`)
		if rr.Code != 501 || decodeErr(t, rr).Code != "not_implemented" {
			t.Errorf("%s: %d %s", m, rr.Code, rr.Body)
		}
	}
}
