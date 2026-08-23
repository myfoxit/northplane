// Package awssig signs HTTP requests with AWS Signature Version 4
// (https://docs.aws.amazon.com/IAM/latest/UserGuide/create-signed-request.html).
// Hand-rolled to keep the dependency tree free of the AWS SDK: the few
// AWS surfaces Northplane talks to (SES e-mail, Polly speech) each need
// exactly one signed request.
package awssig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Credentials identify the caller; SessionToken is optional (STS).
type Credentials struct {
	AccessKey    string
	SecretKey    string
	SessionToken string
	Region       string
	Service      string
}

// Sign adds X-Amz-Date (and X-Amz-Security-Token) plus the Authorization
// header to req for the given payload at time now.
func Sign(req *http.Request, payload []byte, cred Credentials, now time.Time) {
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := HexSHA256(payload)

	req.Header.Set("X-Amz-Date", amzDate)
	if cred.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", cred.SessionToken)
	}

	headers := map[string]string{
		"host":       req.Host,
		"x-amz-date": amzDate,
	}
	if req.Host == "" {
		headers["host"] = req.URL.Host
	}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		headers["content-type"] = ct
	}
	if cred.SessionToken != "" {
		headers["x-amz-security-token"] = cred.SessionToken
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + strings.TrimSpace(headers[k]) + "\n")
	}
	signedHeaders := strings.Join(names, ";")

	canonURI := req.URL.EscapedPath()
	if canonURI == "" {
		canonURI = "/"
	}
	canonRequest := strings.Join([]string{
		req.Method, canonURI, canonicalQuery(req.URL.Query()), canonHeaders.String(), signedHeaders, payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, cred.Region, cred.Service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, HexSHA256([]byte(canonRequest)),
	}, "\n")

	kDate := HMACSHA256([]byte("AWS4"+cred.SecretKey), dateStamp)
	kRegion := HMACSHA256(kDate, cred.Region)
	kService := HMACSHA256(kRegion, cred.Service)
	kSigning := HMACSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(HMACSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		cred.AccessKey, scope, signedHeaders, signature))
}

// canonicalQuery renders the query string per SigV4: keys sorted, values
// URI-encoded with %20 for space.
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k)+"="+uriEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

func uriEncode(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// HexSHA256 returns the lower-case hex SHA-256 of b.
func HexSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// HMACSHA256 returns HMAC-SHA256(key, data).
func HMACSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}
