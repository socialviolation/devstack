package svcconfig

import "strings"

// credentialPairNames are settings-string pair names whose value authenticates.
var credentialPairNames = map[string]bool{
	"password":              true,
	"pwd":                   true,
	"accountkey":            true,
	"sharedaccesskey":       true,
	"sharedaccesssignature": true,
	"sas":                   true,
	"secret":                true,
	"token":                 true,
	"apikey":                true,
	"sig":                   true,
	"code":                  true,
	"credential":            true,
	"certificate":           true,
	"privatekey":            true,
}

// identifyingPairNames are settings-string pair names whose value says what the
// connection points AT — the thing a reader is checking. Anything not listed
// here is redacted: an unrecognized pair may well be carrying a credential.
var identifyingPairNames = map[string]bool{
	"server":                   true,
	"host":                     true,
	"datasource":               true,
	"address":                  true,
	"addr":                     true,
	"networkaddress":           true,
	"database":                 true,
	"initialcatalog":           true,
	"port":                     true,
	"user":                     true,
	"userid":                   true,
	"uid":                      true,
	"username":                 true,
	"accountname":              true,
	"sharedaccesskeyname":      true,
	"endpoint":                 true,
	"endpointsuffix":           true,
	"protocol":                 true,
	"defaultendpointsprotocol": true,
	"ssl":                      true,
	"sslmode":                  true,
	"encrypt":                  true,
	"trustservercertificate":   true,
	"timeout":                  true,
	"pooling":                  true,
	"abortconnect":             true,
	"applicationname":          true,

	"authentication":           true,
	"multipleactiveresultsets": true,
	"connectiontimeout":        true,
	"connectlifetime":          true,
	"connectionlifetime":       true,
	"minpoolsize":              true,
	"maxpoolsize":              true,
	"integratedsecurity":       true,
	"persistsecurityinfo":      true,
	"applicationintent":        true,
	"multisubnetfailover":      true,
	"currentlanguage":          true,
	"packetsize":               true,
	"workstationid":            true,
	"failoverpartner":          true,
	"columnencryptionsetting":  true,
	"attachdbfilename":         true,
	"networklibrary":           true,
	"loadbalancetimeout":       true,
	"enlist":                   true,
	"replication":              true,
	"hostnameincertificate":    true,
	"commandtimeout":           true,

	"entitypath":    true,
	"transporttype": true,
	"blobendpoint":  true,
	"queueendpoint": true,
	"tableendpoint": true,
	"fileendpoint":  true,

	"connectretry":    true,
	"connecttimeout":  true,
	"synctimeout":     true,
	"defaultdatabase": true,
	"allowadmin":      true,
	"keepalive":       true,
	"name":            true,

	"searchpath":         true,
	"sslrootcert":        true,
	"sslcert":            true,
	"channelbinding":     true,
	"targetsessionattrs": true,
}

// RedactValue renders a config value so a reader can tell what it points at
// without seeing what authenticates it: for a connection string the server,
// database and user survive while the password does not. Structured values are
// taken apart pair by pair; only values with no structure left to preserve are
// masked whole.
func RedactValue(key, value string) string {
	switch {
	case strings.Contains(value, ";") && strings.Contains(value, "="):
		return redactPairs(value, ";")
	case strings.Contains(value, "://"):
		return redactURL(value)
	case strings.Contains(value, "&") && strings.Contains(value, "="):
		return redactPairs(value, "&")
	case strings.Contains(value, ",") && strings.Contains(value, "="):
		return redactAttrList(value)
	}
	if IsSecret(key, value) {
		return maskedValue
	}
	return credentialRe.ReplaceAllString(value, "$1="+maskedValue)
}

func redactPairs(value, sep string) string {
	parts := strings.Split(value, sep)
	for i, p := range parts {
		parts[i] = redactPair(p)
	}
	return strings.Join(parts, sep)
}

func redactPair(pair string) string {
	name, val, ok := strings.Cut(pair, "=")
	if !ok || strings.TrimSpace(val) == "" {
		return pair
	}
	n := normalizePairName(name)
	if !credentialPairNames[n] && identifyingPairNames[n] {
		if strings.Contains(val, "://") {
			return name + "=" + redactURL(val)
		}
		return pair
	}
	return name + "=" + maskedValue
}

// redactAttrList handles a comma-delimited attribute list — OTEL resource
// attributes, Redis client options — where a pair carries a credential only if
// it is named as one, so unrecognized names stay readable.
func redactAttrList(value string) string {
	parts := strings.Split(value, ",")
	for i, p := range parts {
		name, val, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(val) == "" {
			continue
		}
		if credentialPairNames[normalizePairName(name)] {
			parts[i] = name + "=" + maskedValue
		}
	}
	return strings.Join(parts, ",")
}

func normalizePairName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r == ' ' || r == '_' || r == '-' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// redactURL removes the password of a scheme://user:pass@host credential and
// applies the pair rules to the query string, leaving scheme, user, host and
// path — the identity of the target — intact.
func redactURL(value string) string {
	out := value
	if i := strings.Index(out, "://"); i >= 0 {
		rest := out[i+3:]
		end := len(rest)
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			end = j
		}
		authority := rest[:end]
		if at := strings.LastIndex(authority, "@"); at >= 0 {
			if c := strings.Index(authority[:at], ":"); c >= 0 {
				out = out[:i+3] + authority[:c+1] + maskedValue + authority[at:] + rest[end:]
			}
		}
	}
	if q := strings.Index(out, "?"); q >= 0 {
		out = out[:q+1] + redactPairs(out[q+1:], "&")
	}
	return out
}
