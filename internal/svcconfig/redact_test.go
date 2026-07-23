package svcconfig

import (
	"strings"
	"testing"
)

func TestRedactValue(t *testing.T) {
	cases := []struct {
		name       string
		key        string
		value      string
		want       string
		credential string
	}{
		{
			name:       "sql server keeps target hides password",
			key:        "ConnectionStrings__App",
			value:      "Server=db.example.com,1433;Initial Catalog=appdb;User ID=svc;Password=hunter2;Encrypt=True",
			want:       "Server=db.example.com,1433;Initial Catalog=appdb;User ID=svc;Password=" + maskedValue + ";Encrypt=True",
			credential: "hunter2",
		},
		{
			name:       "sql server pwd alias",
			key:        "ConnectionStrings__App",
			value:      "Data Source=db.example.com;Initial Catalog=appdb;Uid=svc;Pwd=hunter2;TrustServerCertificate=True",
			want:       "Data Source=db.example.com;Initial Catalog=appdb;Uid=svc;Pwd=" + maskedValue + ";TrustServerCertificate=True",
			credential: "hunter2",
		},
		{
			name:       "azure storage keeps account hides key",
			key:        "Storage__Blobs",
			value:      "DefaultEndpointsProtocol=https;AccountName=acct;AccountKey=abc123;EndpointSuffix=core.windows.net",
			want:       "DefaultEndpointsProtocol=https;AccountName=acct;AccountKey=" + maskedValue + ";EndpointSuffix=core.windows.net",
			credential: "abc123",
		},
		{
			name:       "service bus keeps endpoint and policy name",
			key:        "ServiceBus__Send",
			value:      "Endpoint=sb://ns.servicebus.windows.net/;SharedAccessKeyName=send;SharedAccessKey=abc123",
			want:       "Endpoint=sb://ns.servicebus.windows.net/;SharedAccessKeyName=send;SharedAccessKey=" + maskedValue,
			credential: "abc123",
		},
		{
			name:  "redis without credentials unchanged",
			key:   "Redis__ConnectionString",
			value: "localhost:6379,abortConnect=false",
			want:  "localhost:6379,abortConnect=false",
		},
		{
			name:       "unknown pair name is redacted",
			key:        "Widget__Conn",
			value:      "Server=db.example.com;Mothership=abc123",
			want:       "Server=db.example.com;Mothership=" + maskedValue,
			credential: "abc123",
		},
		{
			name:       "opaque token under secret key masked whole",
			key:        "Api__Token",
			value:      "abc123",
			want:       maskedValue,
			credential: "abc123",
		},
		{
			name:  "plain non-secret value unchanged",
			key:   "PeerServiceUrl",
			value: "https://peer.example.com/health",
			want:  "https://peer.example.com/health",
		},
		{
			name:       "url password redacted host kept",
			key:        "Mongo__Url",
			value:      "mongodb://svc:hunter2@db.example.com:27017/appdb",
			want:       "mongodb://svc:" + maskedValue + "@db.example.com:27017/appdb",
			credential: "hunter2",
		},
		{
			name:       "url query credential redacted",
			key:        "PriceLookupUrl",
			value:      "https://fn.example.com/api/Price?market=asx&code=abc123",
			want:       "https://fn.example.com/api/Price?market=" + maskedValue + "&code=" + maskedValue,
			credential: "abc123",
		},
		{
			name:  "sql server azure ad auth without password fully visible",
			key:   "ConnectionStrings__App",
			value: "Server=db.example.com;Initial Catalog=appdb;Authentication=Active Directory Default;Encrypt=True;TrustServerCertificate=False;Connection Timeout=30",
			want:  "Server=db.example.com;Initial Catalog=appdb;Authentication=Active Directory Default;Encrypt=True;TrustServerCertificate=False;Connection Timeout=30",
		},
		{
			name:       "authentication survives alongside redacted password",
			key:        "ConnectionStrings__App",
			value:      "Server=db.example.com;Authentication=Active Directory Password;User ID=svc;Password=hunter2",
			want:       "Server=db.example.com;Authentication=Active Directory Password;User ID=svc;Password=" + maskedValue,
			credential: "hunter2",
		},
		{
			name:  "mars and connection timeout visible",
			key:   "ConnectionStrings__App",
			value: "MultipleActiveResultSets=True;Connection Timeout=30",
			want:  "MultipleActiveResultSets=True;Connection Timeout=30",
		},
		{
			name:  "otel resource attributes fully visible",
			key:   "OTEL_RESOURCE_ATTRIBUTES",
			value: "devstack.workspace=ws,devstack.service=api,devstack.stack=feat",
			want:  "devstack.workspace=ws,devstack.service=api,devstack.stack=feat",
		},
		{
			name:       "comma list redacts named credentials only",
			key:        "Redis__ConnectionString",
			value:      "localhost:6379,abortConnect=false,password=hunter2,token=abc123,sharedAccessSignature=abc123,name=api",
			want:       "localhost:6379,abortConnect=false,password=" + maskedValue + ",token=" + maskedValue + ",sharedAccessSignature=" + maskedValue + ",name=api",
			credential: "hunter2",
		},
		{
			name:       "shared access signature stays redacted",
			key:        "Storage__Blobs",
			value:      "BlobEndpoint=https://acct.blob.core.windows.net;SharedAccessSignature=sv=2021&sig=abc123",
			want:       "BlobEndpoint=https://acct.blob.core.windows.net;SharedAccessSignature=" + maskedValue,
			credential: "abc123",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactValue(c.key, c.value)
			if got != c.want {
				t.Errorf("RedactValue(%q, %q) = %q, want %q", c.key, c.value, got, c.want)
			}
			if c.credential != "" && strings.Contains(got, c.credential) {
				t.Errorf("credential %q leaked in %q", c.credential, got)
			}
		})
	}
}

func TestRedactValueKeepsExternalMarkerMasked(t *testing.T) {
	if got := RedactValue("ConnectionStrings__App", externalMarker); got != maskedValue {
		t.Errorf("RedactValue(externalMarker) = %q, want it masked", got)
	}
}

func TestRedactValueRedactsStandalonePair(t *testing.T) {
	got := RedactValue("LegacyDsn", "Pwd=hunter2")
	if strings.Contains(got, "hunter2") {
		t.Errorf("standalone credential pair leaked: %q", got)
	}
}
