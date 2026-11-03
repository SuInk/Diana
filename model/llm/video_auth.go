// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// klingJWT 按可灵旧版鉴权签 HS256 JWT：iss 是 AccessKey，30 分钟后过期，生效时间往前
// 拨 5 秒，免得两边时钟差一点就被判「尚未生效」。
// https://klingai.com/document-api/api/get-started/authentication
func klingJWT(accessKey, secretKey string, now time.Time) string {
	header, _ := json.Marshal(struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}{"HS256", "JWT"})
	payload, _ := json.Marshal(struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
		Nbf int64  `json:"nbf"`
	}{accessKey, now.Add(30 * time.Minute).Unix(), now.Add(-5 * time.Second).Unix()})
	encode := base64.RawURLEncoding.EncodeToString
	unsigned := encode(header) + "." + encode(payload)
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(unsigned))
	return unsigned + "." + encode(mac.Sum(nil))
}

// signVolcV4 按火山引擎 HMAC-SHA256 签名写 X-Date 和 Authorization。只签 host 和
// x-date 两个头，和官方示例一致；其余头（Content-Type、User-Agent）之后再写也不影响。
// https://www.volcengine.com/docs/6369/67269
func signVolcV4(req *http.Request, accessKey, secretKey, region, service string, now time.Time) {
	xDate := now.UTC().Format("20060102T150405Z")
	date := xDate[:8]
	req.Header.Set("X-Date", xDate)
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	var body []byte
	if req.GetBody != nil {
		if reader, err := req.GetBody(); err == nil {
			body, _ = io.ReadAll(reader)
			reader.Close()
		}
	}
	bodyHash := sha256.Sum256(body)
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	signedHeaders := "host;x-date"
	canonical := strings.Join([]string{
		req.Method,
		path,
		volcCanonicalQuery(req.URL.Query()),
		"host:" + host + "\nx-date:" + xDate + "\n",
		signedHeaders,
		hex.EncodeToString(bodyHash[:]),
	}, "\n")
	scope := date + "/" + region + "/" + service + "/request"
	canonicalHash := sha256.Sum256([]byte(canonical))
	stringToSign := "HMAC-SHA256\n" + xDate + "\n" + scope + "\n" + hex.EncodeToString(canonicalHash[:])
	key := []byte(secretKey)
	for _, part := range []string{date, region, service, "request"} {
		key = hmacSHA256(key, part)
	}
	signature := hex.EncodeToString(hmacSHA256(key, stringToSign))
	req.Header.Set("Authorization", "HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// volcCanonicalQuery 按键名排序、RFC 3986 转义（空格是 %20 不是 +）。
func volcCanonicalQuery(query url.Values) string {
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	escape := func(value string) string { return strings.ReplaceAll(url.QueryEscape(value), "+", "%20") }
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		values := append([]string(nil), query[key]...)
		sort.Strings(values)
		for _, value := range values {
			parts = append(parts, escape(key)+"="+escape(value))
		}
	}
	return strings.Join(parts, "&")
}
