package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	stdhtml "html" // Standard library html, aliased to avoid conflict
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html" // For HTML parsing and rewriting, will be used via its direct name 'html'
)

// Configuration
var (
	listenPort string
	// Default privacy settings (can be overridden by user preferences later)
	defaultJSEnabled      = false
	defaultCookiesEnabled = false
	defaultIframesEnabled = false

	// authServiceURL will be populated from environment variable
	authServiceURL string
)

// Cookie names
const (
	authCookieName        = "CF_Authorization" // Cookie for this proxy's own auth
	maxRedirects          = 5                  // Max redirects for the proxy to follow internally
	originalURLCookieName = "proxy_original_url"
)

// Regex for parsing forms (used in auth flow)
var (
	formActionRegex    = regexp.MustCompile(`(?is)<form[^>]*action\s*=\s*["']([^"']+)["'][^>]*>`)
	hiddenInputRegex   = regexp.MustCompile(`(?is)<input[^>]*type\s*=\s*["']hidden["'][^>]*name\s*=\s*["']([^"']+)["'][^>]*value\s*=\s*["']([^"']*)["'][^>]*>`)
	nonceInputRegex    = regexp.MustCompile(`(?is)<input[^>]*name\s*=\s*["']nonce["'][^>]*value\s*=\s*["']([^"']+)["']`)
	codeInputFormRegex = regexp.MustCompile(`(?is)<form[^>]*action\s*=\s*["']([^"']*/cdn-cgi/access/callback[^"']*)["'][^>]*>`) // For CF's code page
)

// JWTHeader represents the decoded header of a JWT
type JWTHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

// JWTPayload represents common claims in a JWT payload
type JWTPayload struct {
	Email         string      `json:"email"`
	IdentityNonce string      `json:"identity_nonce"`
	Issuer        string      `json:"iss"`
	Audience      interface{} `json:"aud"` // Can be string or []string
	ExpiresAt     int64       `json:"exp"`
	IssuedAt      int64       `json:"iat"`
	NotBefore     int64       `json:"nbf"`
	Subject       string      `json:"sub"`
	Type          string      `json:"type"`
	Country       string      `json:"country"`
}

// --- Initialization ---
func initEnv() {
	listenPort = os.Getenv("PORT")
	if listenPort == "" {
		listenPort = "8080"
		log.Printf("Warning: PORT environment variable not set, defaulting to %s", listenPort)
	}

	authServiceURL = os.Getenv("AUTH_SERVICE_URL")
	if authServiceURL == "" {
		log.Fatal("Error: AUTH_SERVICE_URL environment variable must be set.")
	}
	// Ensure authServiceURL ends with a slash for proper parsing with relative paths
	if !strings.HasSuffix(authServiceURL, "/") {
		authServiceURL += "/"
	}
	log.Printf("Auth Service URL configured to: %s", authServiceURL)
}

func main() {
	initEnv()

	// Auth flow handlers
	http.HandleFunc("/auth/enter-email", handleServeEmailPage)
	http.HandleFunc("/auth/submit-email", handleSubmitEmailToExternalCF)
	http.HandleFunc("/auth/submit-code", handleSubmitCodeToExternalCF)

	// Main proxy handlers (protected by auth)
	http.HandleFunc("/", masterHandler) // Gatekeeper for / and /proxy

	log.Printf("Starting privacy-centric proxy server with auth on port %s", listenPort)
	log.Printf("Auth flow will be triggered by interacting with: %s", authServiceURL)
	if err := http.ListenAndServe(":"+listenPort, nil); err != nil {
		log.Fatalf("ListenAndServe error: %v", err)
	}
}

// --- Utility Helper Functions (Defined Before Use) ---
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isCFAuthCookieValid(r *http.Request) (isValid bool, payload *JWTPayload, err error) {
	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		return false, nil, nil
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 {
		return false, nil, fmt.Errorf("token is not a valid JWT structure (parts != 3)")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false, nil, fmt.Errorf("failed to base64-decode JWT payload: %w", err)
	}
	var p JWTPayload
	if err := json.Unmarshal(payloadBytes, &p); err != nil {
		return false, nil, fmt.Errorf("failed to unmarshal JWT payload JSON: %w", err)
	}
	now := time.Now().Unix()
	if p.ExpiresAt != 0 && now > p.ExpiresAt {
		return false, &p, fmt.Errorf("token expired at %s", time.Unix(p.ExpiresAt, 0))
	}
	if p.NotBefore != 0 && now < p.NotBefore {
		return false, &p, fmt.Errorf("token not yet valid (nbf: %s)", time.Unix(p.NotBefore, 0))
	}
	return true, &p, nil
}

// parseAndValidateJWT decodes JWT and checks timestamps. Returns payload if valid.
// Does not perform cryptographic signature validation.
func parseAndValidateJWT(cookieValue string) (isValid bool, payload *JWTPayload, err error) {
	// Split the JWT into its three parts: header, payload, signature.
	parts := strings.Split(cookieValue, ".")
	if len(parts) != 3 {
		// A valid JWT must have three parts.
		return false, nil, fmt.Errorf("token is not a valid JWT structure (parts != 3)")
	}

	// The payload is the second part. It's Base64URL encoded.
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// If decoding fails, it's not a valid JWT payload.
		return false, nil, fmt.Errorf("failed to base64-decode JWT payload: %w", err)
	}

	// Unmarshal the decoded payload (which is JSON) into our JWTPayload struct.
	var p JWTPayload
	if err := json.Unmarshal(payloadBytes, &p); err != nil {
		// If unmarshaling fails, the payload JSON is malformed or doesn't match our struct.
		return false, nil, fmt.Errorf("failed to unmarshal JWT payload JSON: %w", err)
	}

	// Check timestamp claims for validity.
	now := time.Now().Unix() // Current time as Unix timestamp.

	// Check 'exp' (ExpiresAt): If the token has an expiration time and it's in the past,
	// the token is expired.
	if p.ExpiresAt != 0 && now > p.ExpiresAt {
		return false, &p, fmt.Errorf("token expired at %s", time.Unix(p.ExpiresAt, 0))
	}

	// Check 'nbf' (NotBefore): If the token has a "not before" time and it's in the future,
	// the token is not yet valid.
	if p.NotBefore != 0 && now < p.NotBefore {
		return false, &p, fmt.Errorf("token not yet valid (nbf: %s)", time.Unix(p.NotBefore, 0))
	}

	// If all checks pass (excluding signature validation), the token is considered valid
	// for the purposes of this function.
	return true, &p, nil
}

func readAndDecompressBody(resp *http.Response) (bodyBytes []byte, wasGzipped bool, err error) {
	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("reading body: %w", err)
	}
	contentEncoding := strings.ToLower(resp.Header.Get("Content-Encoding"))
	if contentEncoding == "gzip" {
		wasGzipped = true
		gzipReader, err := gzip.NewReader(bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, true, fmt.Errorf("creating gzip reader: %w", err)
		}
		defer gzipReader.Close()
		decompressedBytes, err := io.ReadAll(gzipReader)
		if err != nil {
			return nil, true, fmt.Errorf("decompressing gzip body: %w", err)
		}
		return decompressedBytes, true, nil
	}
	return bodyBytes, false, nil
}

func logReasonsForNotAutomating(isHTML bool, statusCode int, hasAuthCookie bool, method string) {
	if !isHTML { log.Printf("  Reason for not automating: Not HTML (Content-Type might be different)") }
	if statusCode != http.StatusOK { log.Printf("  Reason for not automating: Initial response status not OK (Status: %d)", statusCode) }
	if method != http.MethodGet { log.Printf("  Reason for not automating: Original request method was not GET (Method: %s)", method) }
}

func determineClientRedirectPath(cfLocation string) string {
	clientRedirectPath := "/"
	parsedCfLocation, err := url.Parse(cfLocation)
	if err == nil {
		if parsedCfLocation.IsAbs() {
			clientRedirectPath = parsedCfLocation.RequestURI()
			log.Printf("  CF Location is absolute ('%s'). Client redirect path set to: '%s'", cfLocation, clientRedirectPath)
		} else if parsedCfLocation.Path != "" {
			clientRedirectPath = parsedCfLocation.String()
			log.Printf("  CF Location is relative ('%s'). Client redirect path set to: '%s'", cfLocation, clientRedirectPath)
		} else {
			log.Printf("  CF Location ('%s') is not a usable absolute or relative path. Defaulting client redirect to '/'.", cfLocation)
		}
	} else {
		log.Printf("  Error parsing CF Location ('%s'): %v. Defaulting client redirect to '/'.", cfLocation, err)
	}
	if clientRedirectPath == "" { clientRedirectPath = "/" }
	return clientRedirectPath
}

func logEmailPostRequest(req *http.Request, formData string) {
	log.Printf(">>> Sending automated email POST request to Cloudflare:")
	log.Printf("    Method: %s", req.Method)
	log.Printf("    URL: %s", req.URL.String())
	log.Println("    Headers:")
	for name, values := range req.Header {
		for _, value := range values {
			log.Printf("      %s: %s", name, value)
		}
	}
	log.Printf("    Body (Form Data): %s", formData)
}

func logEmailPostResponse(resp *http.Response) {
	log.Printf("<<< Received response from automated email POST:")
	log.Printf("    Status: %s", resp.Status)
	log.Printf("    Final URL of this step: %s", resp.Request.URL.String())
	log.Println("    Headers:")
	for name, values := range resp.Header {
		for _, value := range values {
			log.Printf("      %s: %s", name, value)
			if strings.ToLower(name) == "set-cookie" {
				log.Printf("        -> Relevant Set-Cookie from email POST resp: %s", value)
			}
		}
	}
}

func logCodeSubmitRequest(req *http.Request, formData string) {
	log.Printf(">>> Sending final auth request (code submit) to Cloudflare:")
	log.Printf("    Method: %s", req.Method)
	log.Printf("    URL: %s", req.URL.String())
	log.Println("    Headers:")
	for name, values := range req.Header {
		for _, value := range values {
			log.Printf("      %s: %s", name, value)
		}
	}
	log.Printf("    Body (Form Data): %s", formData)
}

func logCodeSubmitResponse(resp *http.Response) {
	log.Printf("<<< Received response from Cloudflare after code submission:")
	log.Printf("    Status: %s", resp.Status)
	log.Printf("    Actual URL of Response: %s", resp.Request.URL.String())
	log.Println("    Headers:")
	for name, values := range resp.Header {
		for _, value := range values {
			log.Printf("      %s: %s", name, value)
		}
	}
}

func parseGeneralForm(htmlBody string, specificFormRegex *regexp.Regexp) (actionURL string, hiddenFields url.Values, formFound bool) {
	hiddenFields = url.Values{}
	if specificFormRegex != nil {
		matches := specificFormRegex.FindStringSubmatch(htmlBody)
		if len(matches) > 0 {
			if len(matches) > 1 {
				actionURL = matches[1]
			}
			formFound = true
		}
	}
	if actionURL == "" {
		actionMatches := formActionRegex.FindStringSubmatch(htmlBody)
		if len(actionMatches) > 1 {
			actionURL = actionMatches[1]
			formFound = true
		}
	}
	if !formFound {
		log.Println("Warning: Could not find any form tag or extract action URL.")
	}
	hiddenInputMatches := hiddenInputRegex.FindAllStringSubmatch(htmlBody, -1)
	for _, match := range hiddenInputMatches {
		if len(match) == 3 {
			fieldName := stdhtml.UnescapeString(strings.TrimSpace(match[1]))
			fieldValue := stdhtml.UnescapeString(strings.TrimSpace(match[2]))
			hiddenFields.Add(fieldName, fieldValue)
			log.Printf("  Found hidden field: Name='%s', Value='%s'", fieldName, fieldValue)
		}
	}
	if len(hiddenFields) == 0 && formFound {
		log.Println("Warning: Form found, but no hidden fields detected in the HTML body.")
	}
	return
}

// --- Request/Response Manipulation Helpers ---

func setupBasicHeadersForAuth(proxyReq *http.Request, clientReq *http.Request, destHost string) {
	proxyReq.Header.Set("Host", destHost)
	proxyReq.Header.Set("User-Agent", "PrivacyProxyAuthFlow/1.0")
	proxyReq.Header.Set("Accept", "*/*")
	proxyReq.Header.Set("Accept-Language", clientReq.Header.Get("Accept-Language"))
	proxyReq.Header.Set("Accept-Encoding", "gzip, deflate")
	proxyReq.Header.Del("Cookie")

	clientIP := strings.Split(clientReq.RemoteAddr, ":")[0]
	if existingXFF := clientReq.Header.Get("X-Forwarded-For"); existingXFF != "" {
		proxyReq.Header.Set("X-Forwarded-For", existingXFF+", "+clientIP)
	} else {
		proxyReq.Header.Set("X-Forwarded-For", clientIP)
	}
	if clientReq.Header.Get("X-Forwarded-Proto") != "" {
		proxyReq.Header.Set("X-Forwarded-Proto", clientReq.Header.Get("X-Forwarded-Proto"))
	} else if clientReq.TLS != nil {
		proxyReq.Header.Set("X-Forwarded-Proto", "https")
	} else {
		proxyReq.Header.Set("X-Forwarded-Proto", "http")
	}
	proxyReq.Header.Set("X-Forwarded-Host", clientReq.Host)
}

func setupOutgoingHeadersForProxy(proxyReq *http.Request, clientReq *http.Request, targetHost string, cookiesEnabled bool) {
	proxyReq.Header.Set("Host", targetHost)
	proxyReq.Header.Set("User-Agent", "PrivacyProxy/1.0 (github.com/your-repo)")
	proxyReq.Header.Set("Accept", clientReq.Header.Get("Accept"))
	proxyReq.Header.Set("Accept-Language", clientReq.Header.Get("Accept-Language"))
	proxyReq.Header.Set("Accept-Encoding", "gzip, deflate")

	if cookiesEnabled {
		for _, cookie := range clientReq.Cookies() {
			if cookie.Name == authCookieName {
				continue
			}
			proxyReq.AddCookie(cookie)
		}
	} else {
		proxyReq.Header.Del("Cookie")
	}

	proxyReq.Header.Del("X-Forwarded-For")
	proxyReq.Header.Del("X-Real-Ip")
}

func addCookiesToOutgoingRequest(outgoingReq *http.Request, setCookieHeaders []string) {
	if len(setCookieHeaders) == 0 {
		return
	}
	existingCookies := outgoingReq.Header.Get("Cookie")
	tempRespHeader := http.Header{"Set-Cookie": setCookieHeaders}
	dummyResp := http.Response{Header: tempRespHeader}
	var newCookies []string
	if existingCookies != "" {
		newCookies = append(newCookies, existingCookies)
	}
	parsedCookies := make(map[string]string)
	if existingCookies != "" {
		parts := strings.Split(existingCookies, ";")
		for _, part := range parts {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) == 2 {
				parsedCookies[kv[0]] = kv[1]
			}
		}
	}
	addedCount := 0
	for _, cookie := range dummyResp.Cookies() {
		if _, exists := parsedCookies[cookie.Name]; !exists {
			newCookies = append(newCookies, cookie.String())
			addedCount++
		}
	}
	if addedCount > 0 {
		outgoingReq.Header.Set("Cookie", strings.Join(newCookies, "; "))
	}
	log.Printf("  Added/merged %d cookies (from recent Set-Cookie headers) to outgoing request to %s. Final Cookie header: %s", addedCount, outgoingReq.URL.Host, outgoingReq.Header.Get("Cookie"))
}

func rewriteURL(originalAttrURL string, pageBaseURL *url.URL, isNavigation bool, clientReq *http.Request) (string, error) {
	originalAttrURL = strings.TrimSpace(originalAttrURL)
	if originalAttrURL == "" || strings.HasPrefix(originalAttrURL, "javascript:") || strings.HasPrefix(originalAttrURL, "mailto:") || strings.HasPrefix(originalAttrURL, "tel:") || strings.HasPrefix(originalAttrURL, "#") || strings.HasPrefix(originalAttrURL, "data:") {
		return originalAttrURL, nil
	}

	absURL, err := pageBaseURL.Parse(originalAttrURL)
	if err != nil {
		log.Printf("Error parsing attribute URL '%s' against base '%s': %v", originalAttrURL, pageBaseURL.String(), err)
		return originalAttrURL, err
	}

	proxyScheme := "http"
	if clientReq.TLS != nil || clientReq.Header.Get("X-Forwarded-Proto") == "https" {
		proxyScheme = "https"
	}
	proxyAccessURL := fmt.Sprintf("%s://%s/proxy?url=%s", proxyScheme, clientReq.Host, url.QueryEscape(absURL.String()))
	return proxyAccessURL, nil
}

func rewriteHTMLContent(htmlReader io.Reader, pageBaseURL *url.URL, clientReq *http.Request, jsEnabled bool) (io.Reader, error) {
	doc, err := html.Parse(htmlReader)
	if err != nil {
		return nil, fmt.Errorf("HTML parsing error: %w", err)
	}

	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "a" || n.Data == "link" || n.Data == "area" {
				for i, attr := range n.Attr {
					if attr.Key == "href" {
						rewritten, err := rewriteURL(attr.Val, pageBaseURL, true, clientReq)
						if err == nil {
							n.Attr[i].Val = rewritten
						}
					}
				}
			}
			if n.Data == "img" || n.Data == "script" || n.Data == "iframe" || n.Data == "audio" || n.Data == "video" || n.Data == "source" || n.Data == "embed" || n.Data == "track" {
				if n.Data == "script" && !jsEnabled {
					log.Printf("JS disabled: Nullifying src for script tag.")
					for i, attr := range n.Attr {
						if attr.Key == "src" {
							n.Attr[i].Val = "#"
						}
					}
					if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
						n.FirstChild.Data = "// JS disabled by proxy"
					}
				} else {
					for i, attr := range n.Attr {
						if attr.Key == "src" {
							rewritten, err := rewriteURL(attr.Val, pageBaseURL, false, clientReq)
							if err == nil {
								n.Attr[i].Val = rewritten
							}
						}
						if attr.Key == "srcset" {
							log.Printf("Skipping srcset rewrite for: %s (not yet implemented)", attr.Val)
						}
					}
				}
			}
			if n.Data == "form" {
				for i, attr := range n.Attr {
					if attr.Key == "action" {
						actionVal := attr.Val
						if actionVal == "" {
							actionVal = pageBaseURL.RequestURI()
						}
						rewritten, err := rewriteURL(actionVal, pageBaseURL, true, clientReq)
						if err == nil {
							n.Attr[i].Val = rewritten
						}
					}
				}
			}
			if n.Data == "a" || n.Data == "area" {
				for i, attr := range n.Attr {
					if attr.Key == "target" && strings.ToLower(attr.Val) == "_blank" {
						n.Attr[i].Val = "_self"
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return nil, fmt.Errorf("HTML rendering error: %w", err)
	}
	return &buf, nil
}

func passThroughResponse(w http.ResponseWriter, clientRequestHost string, sourceResp *http.Response, bodyBytes []byte, originalSetCookieHeaders []string, wasDecompressed bool) {
	for name, values := range sourceResp.Header {
		lowerName := strings.ToLower(name)
		if lowerName == "content-length" ||
			lowerName == "transfer-encoding" ||
			lowerName == "connection" ||
			(lowerName == "content-encoding" && wasDecompressed) {
			continue
		}
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	for _, cookieHeader := range originalSetCookieHeaders {
		w.Header().Add("Set-Cookie", cookieHeader)
		log.Printf("Relaying Set-Cookie to client (passthrough): %s", cookieHeader)
	}
	w.WriteHeader(sourceResp.StatusCode)
	_, err := w.Write(bodyBytes)
	if err != nil {
		log.Printf("Error writing passthrough response body to client: %v", err)
	}
}

// --- Auth Flow Page Servers ---

func serveCustomCodeInputPage(w http.ResponseWriter, r *http.Request, nonce, cfCallbackURL string, setCookieHeaders []string, cfAccessDomain string) {
	log.Printf("Serving custom code input page for proxy auth. Nonce: %s, CF_Callback: %s, CF_Access_Domain: %s", nonce, cfCallbackURL, cfAccessDomain)
	for _, ch := range setCookieHeaders {
		w.Header().Add("Set-Cookie", ch)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Enter Verification Code</title><style>body{font-family:sans-serif;margin:20px;display:flex;flex-direction:column;align-items:center;padding-top:40px;}.container{border:1px solid #ccc;padding:20px;border-radius:5px;background-color:#f9f9f9;}form > div{margin-bottom:15px;}label{display:inline-block;min-width:120px;margin-bottom:5px;}input[type="text"]{padding:8px;border:1px solid #ddd;border-radius:3px;}button{padding:10px 15px;background-color:#007bff;color:white;border:none;border-radius:3px;cursor:pointer;}button:hover{background-color:#0056b3;}</style></head><body><div class="container"><h2>Enter Verification Code</h2><p>A code was sent to your email. Please enter it below.</p><form action="/auth/submit-code" method="POST"><input type="hidden" name="nonce" value="`)
	sb.WriteString(stdhtml.EscapeString(nonce))
	sb.WriteString(`"><input type="hidden" name="cf_callback_url" value="`)
	sb.WriteString(stdhtml.EscapeString(cfCallbackURL))
	sb.WriteString(`"><div><label for="code">Verification Code:</label><input type="text" id="code" name="code" pattern="\d{6}" title="Enter the 6-digit code" required maxlength="6" inputmode="numeric"></div><div><button type="submit">Submit Code</button></div></form></div></body></html>`)
	fmt.Fprint(w, sb.String())
}

func handleServeEmailPage(w http.ResponseWriter, r *http.Request) {
	log.Println("Serving custom email entry page for proxy auth.")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	originalURL := "/"
	if origURLCookie, err := r.Cookie(originalURLCookieName); err == nil {
		if unescaped, errUnescape := url.QueryUnescape(origURLCookie.Value); errUnescape == nil {
			originalURL = unescaped
		}
	}

	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8"><title>Proxy Authentication - Enter Email</title><style>body{font-family:sans-serif;display:flex;justify-content:center;align-items:center;min-height:90vh;margin:0;background-color:#f0f0f0;}.container{background-color:white;padding:20px 40px;border-radius:8px;box-shadow:0 0 10px #ccc;}label{display:block;margin-bottom:5px;}input[type='email']{width:100%;padding:8px;margin-bottom:10px;border:1px solid #ddd;border-radius:4px;}button{padding:10px 15px;background-color:#007bff;color:white;border:none;border-radius:4px;cursor:pointer;}</style></head><body><div class="container"><h2>Proxy Service Authentication</h2><p>Please enter your email to access the proxy service:</p><form action="/auth/submit-email" method="POST"><input type="hidden" name="original_url" value="`)
	sb.WriteString(stdhtml.EscapeString(originalURL))
	sb.WriteString(`"><label for="email">Email:</label><input type="email" id="email" name="email" required autofocus><button type="submit">Send Verification Code</button></form></div></body></html>`)
	fmt.Fprint(w, sb.String())
}

// --- Auth Flow Submission Handlers ---

func handleSubmitEmailToExternalCF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Error parsing email form: "+err.Error(), http.StatusBadRequest)
		return
	}
	userEmail := r.FormValue("email")
	originalURLPath := r.FormValue("original_url")
	if originalURLPath == "" {
		originalURLPath = "/"
	}
	if userEmail == "" {
		http.Error(w, "Email is required", http.StatusBadRequest)
		return
	}
	log.Printf("Auth: Email submitted by user: %s. Original proxy URL intended: %s", userEmail, originalURLPath)

	log.Printf("Auth: Fetching external CF Access login page from: %s", authServiceURL)
	tempReq, _ := http.NewRequest(http.MethodGet, authServiceURL, nil)
	parsedAuthServiceURL, _ := url.Parse(authServiceURL)
	setupBasicHeadersForAuth(tempReq, r, parsedAuthServiceURL.Host)

	tempClient := &http.Client{}
	cfLoginPageResp, err := tempClient.Do(tempReq)
	if err != nil {
		http.Error(w, "Failed to fetch external CF Access login page: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer cfLoginPageResp.Body.Close()

	var currentSetCookieHeaders = cfLoginPageResp.Header["Set-Cookie"]

	cfLoginPageBodyBytes, _, err := readAndDecompressBody(cfLoginPageResp)
	if err != nil {
		http.Error(w, "Failed to read external CF Access login page body: "+err.Error(), http.StatusInternalServerError)
		return
	}
	htmlBody := string(cfLoginPageBodyBytes)

	formActionRaw, hiddenFields, formFound := parseGeneralForm(htmlBody, nil)
	if !formFound || formActionRaw == "" {
		log.Printf("Could not find form on external CF Access page from %s. Body snippet: %s", cfLoginPageResp.Request.URL.String(), htmlBody[:min(500, len(htmlBody))])
		http.Error(w, "Failed to find email submission form on external Cloudflare page.", http.StatusInternalServerError)
		return
	}
	formActionDecoded := stdhtml.UnescapeString(formActionRaw)
	emailFormActionURL, err := cfLoginPageResp.Request.URL.Parse(formActionDecoded)
	if err != nil {
		log.Printf("Error resolving email form action URL '%s' from external CF page: %v", formActionDecoded, err)
		http.Error(w, "Invalid email submission form action on external Cloudflare page.", http.StatusInternalServerError)
		return
	}
	log.Printf("Auth: Email form action URL from external CF page resolved to: %s", emailFormActionURL.String())

	formData := url.Values{"email": {userEmail}}
	for name, values := range hiddenFields {
		for _, value := range values {
			formData.Add(name, value)
		}
	}
	encodedEmailFormData := formData.Encode()

	automatedPostReq, _ := http.NewRequest(http.MethodPost, emailFormActionURL.String(), strings.NewReader(encodedEmailFormData))
	setupBasicHeadersForAuth(automatedPostReq, r, emailFormActionURL.Host)
	automatedPostReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	automatedPostReq.Header.Set("Origin", fmt.Sprintf("%s://%s", emailFormActionURL.Scheme, emailFormActionURL.Host))
	automatedPostReq.Header.Set("Referer", cfLoginPageResp.Request.URL.String())
	automatedPostReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(encodedEmailFormData)))
	addCookiesToOutgoingRequest(automatedPostReq, currentSetCookieHeaders)

	logEmailPostRequest(automatedPostReq, encodedEmailFormData)

	emailSubmitClient := &http.Client{}
	respAfterEmailPost, err := emailSubmitClient.Do(automatedPostReq)
	if err != nil {
		log.Printf("Error POSTing email to external CF Access %s: %v", emailFormActionURL.String(), err)
		http.Error(w, "Failed to submit email to external Cloudflare: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer respAfterEmailPost.Body.Close()

	logEmailPostResponse(respAfterEmailPost)
	currentSetCookieHeaders = append(currentSetCookieHeaders, respAfterEmailPost.Header["Set-Cookie"]...)

	bodyAfterEmailPost, _, err := readAndDecompressBody(respAfterEmailPost)
	if err != nil {
		http.Error(w, "Error reading response after email POST to external CF: "+err.Error(), http.StatusInternalServerError)
		return
	}
	htmlAfterEmailPost := string(bodyAfterEmailPost)

	codeFormActionRaw, codeFormHiddenFields, codeFormFound := parseGeneralForm(htmlAfterEmailPost, codeInputFormRegex)
	var nonceValue string
	nonceMatches := nonceInputRegex.FindStringSubmatch(htmlAfterEmailPost)
	if len(nonceMatches) > 1 {
		nonceValue = stdhtml.UnescapeString(nonceMatches[1])
		if _, ok := codeFormHiddenFields["nonce"]; !ok {
			codeFormHiddenFields.Add("nonce", nonceValue)
		}
	} else if val, ok := codeFormHiddenFields["nonce"]; ok && len(val) > 0 {
		nonceValue = val[0]
	}

	if codeFormFound && nonceValue != "" && (strings.Contains(htmlAfterEmailPost, "Enter code") || strings.Contains(htmlAfterEmailPost, "Enter the code")) {
		log.Println("Auth: Detected 'Enter Code' page from external CF. Serving custom code input page.")
		codeFormActionDecoded := stdhtml.UnescapeString(codeFormActionRaw)
		baseForCodeCallback := respAfterEmailPost.Request.URL
		parsedCodeCallbackURL, err := baseForCodeCallback.Parse(codeFormActionDecoded)
		if err != nil {
			log.Printf("Auth: Error resolving code callback URL '%s': %v", codeFormActionDecoded, err)
			http.Error(w, "Invalid code submission form action on external Cloudflare page.", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: originalURLCookieName, Value: url.QueryEscape(originalURLPath), Path: "/", HttpOnly: true, MaxAge: 300})
		serveCustomCodeInputPage(w, r, nonceValue, parsedCodeCallbackURL.String(), currentSetCookieHeaders, baseForCodeCallback.Host)
		return
	}

	log.Println("Auth: Did not detect 'Enter Code' page after email submission to external CF. Content received:")
	log.Println(htmlAfterEmailPost[:min(1000, len(htmlAfterEmailPost))])
	http.Error(w, "Failed to reach the 'Enter Code' page from external Cloudflare. Please try again.", http.StatusInternalServerError)
}

func handleSubmitCodeToExternalCF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Error parsing code form: "+err.Error(), http.StatusBadRequest)
		return
	}
	userCode := r.FormValue("code")
	nonce := r.FormValue("nonce")
	cfCallbackURLString := r.FormValue("cf_callback_url")

	if userCode == "" || nonce == "" || cfCallbackURLString == "" {
		http.Error(w, "Missing code, nonce, or callback URL", http.StatusBadRequest)
		return
	}
	log.Printf("Auth: Received code for external CF. Code: %s, Nonce: %s, CF_Callback_URL: %s", userCode, nonce, cfCallbackURLString)

	cfFormData := url.Values{"code": {userCode}, "nonce": {nonce}}
	encodedCfFormData := cfFormData.Encode()

	currentRedirectURLString := cfCallbackURLString
	var accumulatedSetCookies []string

	loopClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			log.Printf(">>> Auth redirect loop: Client was about to redirect from %s to %s", via[len(via)-1].URL.String(), req.URL.String())
			return http.ErrUseLastResponse
		},
	}
	var finalLoopResponse *http.Response

	for i := 0; i < maxRedirects; i++ {
		log.Printf("Auth redirect loop (Attempt %d): Requesting %s", i+1, currentRedirectURLString)
		var reqToFollow *http.Request
		var err error

		if i == 0 {
			reqToFollow, err = http.NewRequest(http.MethodPost, currentRedirectURLString, strings.NewReader(encodedCfFormData))
			if err != nil {
				http.Error(w, "Error creating POST for code to external CF: "+err.Error(), http.StatusInternalServerError)
				return
			}
			reqToFollow.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			reqToFollow.Header.Set("Content-Length", fmt.Sprintf("%d", len(encodedCfFormData)))
		} else {
			reqToFollow, err = http.NewRequest(http.MethodGet, currentRedirectURLString, nil)
			if err != nil {
				http.Error(w, "Error creating GET for redirect to external CF: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}

		parsedCurrentURL, _ := url.Parse(currentRedirectURLString)
		setupBasicHeadersForAuth(reqToFollow, r, parsedCurrentURL.Host)
		for _, c := range r.Cookies() {
			if c.Name != originalURLCookieName && c.Name != authCookieName {
				reqToFollow.AddCookie(c)
			}
		}
		addCookiesToOutgoingRequest(reqToFollow, accumulatedSetCookies)

		if i == 0 {
			logCodeSubmitRequest(reqToFollow, encodedCfFormData)
		} else {
			log.Printf(">>> Auth redirect loop (Attempt %d) GET %s", i+1, currentRedirectURLString)
		}

		resp, err := loopClient.Do(reqToFollow)
		if err != nil {
			log.Printf("Error in auth redirect loop (Attempt %d) for %s: %v", i+1, currentRedirectURLString, err)
			if resp == nil {
				http.Error(w, "Error during external CF redirect following: "+err.Error(), http.StatusBadGateway)
				return
			}
		}

		log.Printf("<<< Auth redirect loop (Attempt %d) Response from %s: Status %s", i+1, resp.Request.URL.String(), resp.Status)
		logCodeSubmitResponse(resp)

		if sc := resp.Header["Set-Cookie"]; len(sc) > 0 {
			log.Printf("    Accumulating %d Set-Cookie headers from external CF step.", len(sc))
			accumulatedSetCookies = append(accumulatedSetCookies, sc...)
		}

		finalLoopResponse = resp

		if resp.StatusCode >= 300 && resp.StatusCode <= 308 && resp.StatusCode != http.StatusNotModified {
			location := resp.Header.Get("Location")
			if location == "" {
				log.Printf("Auth redirect status %d but no Location header. Breaking loop.", resp.StatusCode)
				resp.Body.Close()
				break
			}
			resolvedLocationURL, err := resp.Request.URL.Parse(location)
			if err != nil {
				log.Printf("Error parsing external CF redirect Location '%s': %v. Breaking loop.", location, err)
				resp.Body.Close()
				break
			}
			currentRedirectURLString = resolvedLocationURL.String()
			log.Printf("    Following external CF redirect to: %s", currentRedirectURLString)
			resp.Body.Close()
			continue
		} else {
			log.Printf("Auth redirect loop finished. Final status from external CF: %s", resp.Status)
			break
		}
	}

	if finalLoopResponse == nil {
		log.Println("Auth: Error: No final response obtained from external CF redirect loop.")
		http.Error(w, "Failed to complete authentication with external Cloudflare service.", http.StatusInternalServerError)
		return
	}
	defer finalLoopResponse.Body.Close()

	var actualCfAuthJWTValue string
	var decodedJWTPayload *JWTPayload
	for _, cookieStr := range accumulatedSetCookies {
		if strings.HasPrefix(cookieStr, authCookieName+"=") {
			parts := strings.SplitN(cookieStr, ";", 2)
			kv := strings.SplitN(parts[0], "=", 2)
			if len(kv) == 2 {
				actualCfAuthJWTValue = kv[1]
				_, decodedJWTPayload, _ = parseAndValidateJWT(actualCfAuthJWTValue)
				break
			}
		}
	}

	if actualCfAuthJWTValue != "" {
		log.Printf("Auth: Successfully obtained actual CF_Authorization JWT from external CF. Value: %s...", actualCfAuthJWTValue[:min(30, len(actualCfAuthJWTValue))])
		var cfAuthCookieToSet string
		for _, cStr := range accumulatedSetCookies {
			if strings.HasPrefix(cStr, authCookieName+"=") {
				cfAuthCookieToSet = cStr
				break
			}
		}
		if cfAuthCookieToSet != "" {
			header := http.Header{}
			header.Add("Set-Cookie", cfAuthCookieToSet)
			dummyResp := http.Response{Header: header}
			parsedCookies := dummyResp.Cookies()
			if len(parsedCookies) > 0 {
				authCookie := parsedCookies[0]
				authCookie.Domain = ""
				authCookie.Path = "/"
				authCookie.Secure = r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
				http.SetCookie(w, authCookie)
				log.Printf("Auth: Set proxy's %s cookie using JWT from external CF.", authCookieName)
			} else {
				log.Printf("Auth: Could not parse the full CF_Authorization Set-Cookie string: %s", cfAuthCookieToSet)
				http.SetCookie(w, &http.Cookie{Name: authCookieName, Value: actualCfAuthJWTValue, Path: "/", HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteLaxMode})
			}
		} else {
			log.Println("Auth: Could not find full Set-Cookie string for CF_Authorization to relay.")
			http.Error(w, "Authentication cookie processing error.", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		http.SetCookie(w, &http.Cookie{Name: originalURLCookieName, Value: "", Path: "/", MaxAge: -1})

		var body strings.Builder
		body.WriteString("<h1>Proxy Authentication Successful!</h1><p>You can now use the proxy service.</p>")
		if decodedJWTPayload != nil {
			body.WriteString("<h2>Decoded JWT Payload (from external CF):</h2><pre>")
			payloadBytes, _ := json.MarshalIndent(decodedJWTPayload, "", "  ")
			body.WriteString(stdhtml.EscapeString(string(payloadBytes)))
			body.WriteString("</pre>")
		}
		originalURLPath := "/"
		if origURLCookie, errCookie := r.Cookie(originalURLCookieName); errCookie == nil {
			if unescaped, errUnescape := url.QueryUnescape(origURLCookie.Value); errUnescape == nil {
				originalURLPath = unescaped
			}
		}
		body.WriteString(fmt.Sprintf("<p><a href=\"%s\">Continue to your page</a> or <a href=\"/\">Go to Proxy Home</a></p>", stdhtml.EscapeString(originalURLPath)))
		fmt.Fprint(w, body.String())
	} else {
		log.Println("Auth: CF_Authorization JWT not found in accumulated cookies after external CF code submission.")
		finalBodyBytes, _, _ := readAndDecompressBody(finalLoopResponse)
		passThroughResponse(w, r.Host, finalLoopResponse, finalBodyBytes, accumulatedSetCookies, false)
	}
}

// --- Privacy Proxy Core Handlers & Helpers ---

func handleLandingPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'self'; form-action 'self';")
	fmt.Fprint(w, landingPageHTML)
}

func handleProxyContent(w http.ResponseWriter, r *http.Request) {
	targetURLString := r.URL.Query().Get("url")
	if targetURLString == "" {
		http.Error(w, "Missing 'url' query parameter for proxy", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(targetURLString, "http://") && !strings.HasPrefix(targetURLString, "https://") {
		targetURLString = "http://" + targetURLString
	}
	targetURL, err := url.Parse(targetURLString)
	if err != nil || (targetURL.Scheme != "http" && targetURL.Scheme != "https") || targetURL.Host == "" {
		http.Error(w, "Invalid target URL for proxy: "+err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("handleProxyContent: Proxying for %s", targetURL.String())

	jsEnabled := defaultJSEnabled
	cookiesEnabled := defaultCookiesEnabled

	proxyReq, err := http.NewRequest(r.Method, targetURL.String(), r.Body)
	if err != nil {
		http.Error(w, "Error creating target request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	setupOutgoingHeadersForProxy(proxyReq, r, targetURL.Host, cookiesEnabled)

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	targetResp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("Error fetching target URL %s: %v", targetURL.String(), err)
		http.Error(w, "Error fetching content from target server: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer targetResp.Body.Close()

	log.Printf("Received response from target %s: Status %s", targetURL.String(), targetResp.Status)

	for name, values := range targetResp.Header {
		lowerName := strings.ToLower(name)
		if lowerName == "set-cookie" && !cookiesEnabled {
			log.Printf("Blocking Set-Cookie from target %s due to privacy setting.", targetURL.Host)
			continue
		}
		if lowerName == "content-security-policy" || lowerName == "x-frame-options" {
			log.Printf("Skipping original header '%s' from %s", name, targetURL.Host)
			continue
		}
		if lowerName == "location" && (targetResp.StatusCode >= 300 && targetResp.StatusCode <= 308) {
			originalLocation := values[0]
			rewrittenLocation, err := rewriteURL(originalLocation, targetURL, true, r)
			if err == nil {
				w.Header().Set(name, rewrittenLocation)
			} else {
				log.Printf("Error rewriting Location header '%s': %v", originalLocation, err)
			}
			continue
		}
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}

	csp := "default-src 'self'; object-src 'none'; base-uri 'self';"
	if jsEnabled {
		csp += fmt.Sprintf(" script-src 'self' %s://%s 'unsafe-inline' 'unsafe-eval';", targetURL.Scheme, targetURL.Host)
	} else {
		csp += " script-src 'none';"
	}
	w.Header().Set("Content-Security-Policy", csp)

	contentType := targetResp.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "text/html") {
		bodyBytes, err := io.ReadAll(targetResp.Body)
		if err != nil {
			http.Error(w, "Error reading HTML body: "+err.Error(), http.StatusInternalServerError)
			return
		}
		var reader io.Reader = bytes.NewReader(bodyBytes)
		if targetResp.Header.Get("Content-Encoding") == "gzip" {
			gzReader, errGzip := gzip.NewReader(reader)
			if errGzip != nil {
				http.Error(w, "Error creating gzip reader: "+errGzip.Error(), http.StatusInternalServerError)
				return
			}
			defer gzReader.Close()
			reader = gzReader
			w.Header().Del("Content-Encoding")
			w.Header().Del("Content-Length")
		}
		rewrittenHTML, errRewrite := rewriteHTMLContent(reader, targetURL, r, jsEnabled)
		if errRewrite != nil {
			http.Error(w, "Error rewriting HTML: "+errRewrite.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(targetResp.StatusCode)
		io.Copy(w, rewrittenHTML)
	} else if strings.HasPrefix(contentType, "text/css") {
		log.Printf("Passing through CSS from %s (rewriting not yet implemented)", targetURL.String())
		w.WriteHeader(targetResp.StatusCode)
		io.Copy(w, targetResp.Body)
	} else {
		if strings.Contains(contentType, "javascript") && !jsEnabled {
			log.Printf("Blocking JavaScript content from %s due to privacy setting.", targetURL.Host)
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "// JavaScript execution disabled by proxy.")
			return
		}
		log.Printf("Passing through content type '%s' from %s", contentType, targetURL.String())
		w.WriteHeader(targetResp.StatusCode)
		io.Copy(w, targetResp.Body)
	}
}

// --- Master Handler (Auth Gatekeeper) ---
func masterHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("masterHandler: Path %s", r.URL.Path)

	isValidAuth, _, validationErr := isCFAuthCookieValid(r)
	if validationErr != nil {
		log.Printf("CF_Authorization cookie validation error for %s: %v. Initiating auth.", r.URL.Path, validationErr)
	}

	if !isValidAuth {
		if !strings.HasPrefix(r.URL.Path, "/auth/") {
			isLikelyHTMLRequest := strings.Contains(r.Header.Get("Accept"), "text/html") ||
				r.Header.Get("Accept") == "" || r.Header.Get("Accept") == "*/*"

			if r.Method == http.MethodGet && (r.URL.Path == "/" || isLikelyHTMLRequest) {
				log.Printf("CF_Authorization invalid/missing for %s. Redirecting to /auth/enter-email.", r.URL.Path)
				originalURL := r.URL.RequestURI()
				http.SetCookie(w, &http.Cookie{
					Name:     originalURLCookieName,
					Value:    url.QueryEscape(originalURL),
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
					MaxAge:   300,
				})
				http.Redirect(w, r, "/auth/enter-email", http.StatusFound)
				return
			} else if r.Method != http.MethodGet {
				log.Printf("CF_Authorization invalid/missing for non-GET request (%s %s). Returning 401.", r.Method, r.URL.Path)
				http.Error(w, "Unauthorized: Authentication required.", http.StatusUnauthorized)
				return
			}
			if r.URL.Path == "/proxy" {
				log.Printf("CF_Authorization invalid/missing for /proxy GET. Returning 401.", r.URL.Path)
				http.Error(w, "Unauthorized: Authentication required to use proxy.", http.StatusUnauthorized)
				return
			}
		}
	}

	if r.URL.Path == "/" {
		handleLandingPage(w, r)
	} else if r.URL.Path == "/proxy" {
		handleProxyContent(w, r)
	} else if !strings.HasPrefix(r.URL.Path, "/auth/") {
		http.NotFound(w, r)
	}
}


// --- Embedded HTML for Landing Page ---
const landingPageHTML = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Privacy Web Proxy</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; margin: 0; padding: 0; background-color: #f4f6f8; display: flex; justify-content: center; align-items: center; min-height: 100vh; color: #333; }
        .container { background-color: #fff; padding: 30px 40px; border-radius: 8px; box-shadow: 0 4px 12px rgba(0,0,0,0.1); text-align: center; width: 100%; max-width: 500px; }
        h1 { color: #2c3e50; margin-bottom: 25px; font-size: 24px; }
        form { display: flex; flex-direction: column; gap: 15px; }
        input[type="url"] { padding: 12px; border: 1px solid #dcdfe6; border-radius: 4px; font-size: 16px; box-sizing: border-box; width: 100%; }
        input[type="url"]:focus { border-color: #409eff; box-shadow: 0 0 0 2px rgba(64, 158, 255, 0.2); outline: none; }
        button[type="submit"] { padding: 12px 20px; background-color: #409eff; color: white; border: none; border-radius: 4px; font-size: 16px; cursor: pointer; transition: background-color 0.2s; }
        button[type="submit"]:hover { background-color: #66b1ff; }
        .settings-placeholder, .bookmarks-placeholder { margin-top: 30px; font-size: 14px; color: #777; }
		.footer { margin-top: 30px; font-size: 12px; color: #aaa; }
    </style>
</head>
<body>
    <div class="container">
        <h1>Privacy Web Proxy</h1>
        <form action="/proxy" method="GET">
            <input type="url" name="url" placeholder="Enter website URL (e.g., example.com)" required autofocus>
            <button type="submit">Go</button>
        </form>
        <div class="settings-placeholder">
            <p>Global privacy settings (JS, Cookies, Iframes) will be managed here.</p>
        </div>
        <div class="bookmarks-placeholder">
            <p>Bookmarks with per-site settings will appear here.</p>
        </div>
		<div class="footer">
			<p>This proxy enhances privacy. Use responsibly.</p>
		</div>
    </div>
	</body>
</html>
`

/*
Example app.yaml for Google App Engine Standard:

runtime: go122

handlers:
- url: /auth/.* # Auth specific handlers
  script: auto
  secure: always

- url: /proxy # Proxy handler
  script: auto
  secure: always

- url: / # Landing page
  script: auto
  secure: always

# - url: /static # If you have static assets like CSS/JS for the proxy UI
#   static_dir: static
#   secure: always

env_variables:
  AUTH_SERVICE_URL: "https://auth-service.workers.dev/" # IMPORTANT: Set this to your CF Access protected service
  PORT: "8080" # App Engine sets this automatically. For local, you can set it.
*/
