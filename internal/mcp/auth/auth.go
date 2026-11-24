package auth

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
)

type Scope string

const (
	ScopeOwner Scope = "owner"
	ScopeAgent Scope = "agent"
)

type Caller struct {
	Name  string
	Scope Scope
}

type callerKey struct{}

func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

func CallerFrom(ctx context.Context) Caller {
	if c, ok := ctx.Value(callerKey{}).(Caller); ok {
		return c
	}
	return Caller{Name: "local", Scope: ScopeOwner}
}

type Token struct {
	Caller Caller
	Hash   []byte
}

const (
	TokensFileEnv = "DEVSTACK_MCP_TOKENS_FILE"
	TokensEnv     = "DEVSTACK_MCP_TOKENS"
)

func parseScope(s string) (Scope, error) {
	switch Scope(s) {
	case ScopeOwner, ScopeAgent:
		return Scope(s), nil
	}
	return "", fmt.Errorf("unknown scope %q: want %q or %q", s, ScopeOwner, ScopeAgent)
}

func LoadTokens() ([]Token, error) {
	var tokens []Token
	if path := os.Getenv(TokensFileEnv); path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", TokensFileEnv, err)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for n := 1; sc.Scan(); n++ {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) != 3 {
				return nil, fmt.Errorf("%s line %d: want '<scope> <name> <sha256-hex>'", path, n)
			}
			scope, err := parseScope(fields[0])
			if err != nil {
				return nil, fmt.Errorf("%s line %d: %w", path, n, err)
			}
			hash, err := hex.DecodeString(fields[2])
			if err != nil || len(hash) != sha256.Size {
				return nil, fmt.Errorf("%s line %d: the hash is not 64 hex characters of sha256", path, n)
			}
			tokens = append(tokens, Token{Caller: Caller{Name: fields[1], Scope: scope}, Hash: hash})
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	for _, entry := range strings.Split(os.Getenv(TokensEnv), ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 3)
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			return nil, fmt.Errorf("%s: want 'scope:name:token' entries, separated by commas", TokensEnv)
		}
		scope, err := parseScope(parts[0])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", TokensEnv, err)
		}
		sum := sha256.Sum256([]byte(parts[2]))
		tokens = append(tokens, Token{Caller: Caller{Name: parts[1], Scope: scope}, Hash: sum[:]})
	}
	return tokens, nil
}

func match(tokens []Token, raw string) (Caller, bool) {
	sum := sha256.Sum256([]byte(raw))
	var found Caller
	ok := false
	for _, t := range tokens {
		if subtle.ConstantTimeCompare(sum[:], t.Hash) == 1 {
			found, ok = t.Caller, true
		}
	}
	return found, ok
}

func Middleware(tokens []Token, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		caller, ok := match(tokens, raw)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), caller)))
	})
}
