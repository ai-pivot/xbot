// qiniu-cert manages Qiniu CDN SSL certificates via the fusion API:
// upload a certificate, bind it to a CDN domain (enable/refresh HTTPS),
// list certificates, and verify the live TLS chain of a domain.
//
// Auth: reads Qiniu AK/SK from ~/.xbot/config.json (oss.qiniu_access_key /
// oss.qiniu_secret_key) — the same credentials xbot uses for uploads.
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qiniu/go-sdk/v7/auth"

	"xbot/config"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ak, sk, err := qiniuCreds()
	if err != nil {
		fatalf("%v", err)
	}
	mac := auth.New(ak, sk)
	client := &http.Client{Timeout: 30 * time.Second}
	switch os.Args[1] {
	case "list":
		call(client, mac, "GET", "/sslcert?limit=100", nil)
	case "upload":
		upload(client, mac, os.Args[2:])
	case "bind":
		bind(client, mac, os.Args[2:])
	case "domain":
		callHost(client, mac, "https://api.qiniu.com", "GET", "/domain/"+arg(os.Args[2:], "domain"), nil)
	case "verify":
		verify(arg(os.Args[2:], "domain"))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `qiniu-cert <command> [flags]

  list                                   list certificates in the account
  upload --cert F --key F --name NAME [--common-name D]\n                                         upload a certificate (Nginx fullchain + key)
  bind --domain D --certid ID [--no-force-https] [--http2]
                                         bind cert to a CDN domain (enables HTTPS)
  domain --domain D                      show CDN domain config (protocol, cname)
  verify --domain D                      check the live TLS certificate of D`)
	os.Exit(2)
}

func qiniuCreds() (string, string, error) {
	cfg := config.LoadFromFile(config.ConfigFilePath())
	if cfg == nil || cfg.OSS.QiniuAccessKey == "" || cfg.OSS.QiniuSecretKey == "" {
		return "", "", fmt.Errorf("qiniu credentials not found in %s (oss.qiniu_access_key/secret_key)", config.ConfigFilePath())
	}
	return cfg.OSS.QiniuAccessKey, cfg.OSS.QiniuSecretKey, nil
}

func arg(args []string, name string) string {
	for i, a := range args {
		if a == "--"+name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--"+name {
			return true
		}
	}
	return false
}

func call(client *http.Client, mac *auth.Credentials, method, path string, body any) map[string]any {
	return callHost(client, mac, "https://fusion.qiniuapi.com", method, path, body)
}

func callHost(client *http.Client, mac *auth.Credentials, host, method, path string, body any) map[string]any {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, host+path, reader)
	if err != nil {
		fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	mac.AddToken(auth.TokenQBox, req)
	resp, err := client.Do(req)
	if err != nil {
		fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	if resp.StatusCode != 200 {
		fatalf("%s %s: HTTP %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	pretty, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(pretty))
	return out
}

func upload(client *http.Client, mac *auth.Credentials, args []string) {
	certPath, keyPath := arg(args, "cert"), arg(args, "key")
	name := arg(args, "name")
	if certPath == "" || keyPath == "" {
		fatalf("upload requires --cert <fullchain.pem> --key <privkey.pem> [--name NAME]")
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		fatalf("read cert: %v", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		fatalf("read key: %v", err)
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(certPath), filepath.Ext(certPath))
	}
	// Qiniu fusion API expects {name, common_name, pri (private key), ca (fullchain)}.
	commonName := arg(args, "common-name")
	if commonName == "" {
		commonName = name
	}
	resp := call(client, mac, "POST", "/sslcert", map[string]string{
		"name": name, "common_name": commonName,
		"pri": string(keyPEM), "ca": string(certPEM),
	})
	// Qiniu returns {"certID": "..."} (capital ID) for uploads, while the
	// cert-detail endpoint uses lowercase "certid" — accept both.
	for _, k := range []string{"certID", "certid"} {
		if id, ok := resp[k].(string); ok && id != "" {
			fmt.Printf("CERTID=%s\n", id)
			return
		}
	}
	if c, ok := resp["cert"].(map[string]any); ok {
		for _, k := range []string{"certID", "certid"} {
			if id, ok := c[k].(string); ok && id != "" {
				fmt.Printf("CERTID=%s\n", id)
				return
			}
		}
	}
	fatalf("upload response had no certID: %v", resp)
}

func bind(client *http.Client, mac *auth.Credentials, args []string) {
	domain, certid := arg(args, "domain"), arg(args, "certid")
	if domain == "" || certid == "" {
		fatalf("bind requires --domain D --certid ID")
	}
	body := map[string]any{
		"certid":      certid,
		"forceHttps":  !hasFlag(args, "no-force-https"),
		"http2Enable": hasFlag(args, "http2"),
	}
	// The HTTPS-config endpoint lives on api.qiniu.com (domain management), and
	// Qiniu historically accepted both /httpsconf (refresh) and /sslize
	// (enable). Probe all combinations and report each status.
	for _, host := range []string{"https://api.qiniu.com", "https://fusion.qiniuapi.com"} {
		for _, path := range []string{"/domain/" + domain + "/sslize", "/domain/" + domain + "/httpsconf"} {
			status, out := tryCall(client, mac, host, "PUT", path, body)
			fmt.Printf("%-32s %-45s -> %d %s\n", host, path, status, out)
			if status == 200 {
				fmt.Printf("BOUND via %s%s (forceHttps=%v http2=%v)\n", host, path, body["forceHttps"], body["http2Enable"])
				return
			}
		}
	}
	fatalf("bind failed on every endpoint")
}

// tryCall performs a request WITHOUT exiting on non-200 (probe helper).
func tryCall(client *http.Client, mac *auth.Credentials, host, method, path string, body any) (int, string) {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, host+path, reader)
	if err != nil {
		return 0, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	mac.AddToken(auth.TokenQBox, req)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	return resp.StatusCode, strings.TrimSpace(string(data))
}

func verify(domain string) {
	if domain == "" {
		fatalf("verify requires --domain D")
	}
	conn, err := tls.Dial("tcp", domain+":443", &tls.Config{ServerName: domain, InsecureSkipVerify: false})
	if err != nil {
		fmt.Printf("verify %s: TLS FAILED: %v\n", domain, err)
		os.Exit(1)
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		fatalf("no peer certificates")
	}
	c := certs[0]
	fmt.Printf("verify %s: OK\n  subject: %s\n  issuer:  %s\n  dns:     %v\n  valid:   %s → %s\n",
		domain, c.Subject.CommonName, c.Issuer.CommonName, c.DNSNames,
		c.NotBefore.Format(time.RFC3339), c.NotAfter.Format(time.RFC3339))
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
